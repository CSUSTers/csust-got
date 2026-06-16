//go:build !386 && !arm

package chatv2

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"time"

	"csust-got/config"
	"csust-got/util"

	"github.com/cloudwego/eino/schema"
	"go.uber.org/zap"
	tb "gopkg.in/telebot.v3"
)

// StreamToTelegram reads from an eino StreamReader and streams the output to a Telegram message.
// If existingMsg is provided (e.g. from progress placeholder), it reuses that message instead
// of creating a new one. Returns the final visible response text, reasoning content, and any error.
func StreamToTelegram(
	ctx context.Context,
	tbCtx tb.Context,
	reader *schema.StreamReader[*schema.Message],
	format *config.ChatOutputFormatConfig,
	existingMsg *tb.Message,
	richEnabled bool,
) (response string, reasoning string, sentMsg *tb.Message, err error) {
	tc := GetTurnContext(ctx) // may be nil outside chatv2
	sp := &streamProcessor{
		ctx:            ctx,
		tbCtx:          tbCtx,
		reader:         reader,
		format:         format,
		richEnabled:    richEnabled,
		rawCaller:      tbCtx.Bot(),
		sentenceDelims: config.BotConfig.SentenceDelimiters,
		editInterval:   getEditInterval(format),
		done:           make(chan struct{}),
		tc:             tc,
	}

	return sp.process(existingMsg)
}

// streamProcessor manages the streaming output lifecycle.
type streamProcessor struct {
	ctx            context.Context
	tbCtx          tb.Context
	reader         *schema.StreamReader[*schema.Message]
	format         *config.ChatOutputFormatConfig
	richEnabled    bool
	rawCaller      telegramRawCaller
	sentenceDelims []string
	editInterval   time.Duration

	// State protected by mutex
	mu               sync.RWMutex
	fullResponse     strings.Builder
	reasoningContent strings.Builder
	placeholderMsg   *tb.Message
	tc               *TurnContext // For editMu locking and lifecycle flags

	// Ticker control
	done chan struct{}
	wg   sync.WaitGroup
}

const defaultEditInterval = 3 * time.Second
const streamControlClearOutputKey = "csust-got:clear-stream-output"

func newClearStreamOutputMessage() *schema.Message {
	return &schema.Message{Extra: map[string]any{streamControlClearOutputKey: true}}
}

func isClearStreamOutputMessage(msg *schema.Message) bool {
	if msg == nil || msg.Extra == nil {
		return false
	}
	shouldClear, _ := msg.Extra[streamControlClearOutputKey].(bool)
	return shouldClear
}

func getEditInterval(format *config.ChatOutputFormatConfig) time.Duration {
	d := format.GetEditInterval()
	if d <= 0 {
		d = defaultEditInterval
	}
	return d
}

// process is the main streaming loop.
func (sp *streamProcessor) process(existingMsg *tb.Message) (string, string, *tb.Message, error) {
	if existingMsg != nil {
		// Reuse existing progress placeholder message
		sp.placeholderMsg = existingMsg
	} else {
		// Send new placeholder message
		parseMode := GetParseMode(sp.format)
		sent, err := util.SendMessageWithError(
			sp.tbCtx.Chat(),
			"...",
			&tb.SendOptions{ParseMode: parseMode, ReplyTo: sp.tbCtx.Message()},
		)
		if err != nil {
			return "", "", nil, err
		}
		sp.placeholderMsg = sent
	}
	// Start periodic update ticker
	ticker := time.NewTicker(sp.editInterval)
	defer ticker.Stop()
	sp.wg.Add(1)
	go sp.tickerLoop(ticker)
	for {
		msg, recvErr := sp.reader.Recv()
		if errors.Is(recvErr, io.EOF) {
			break
		}
		if recvErr != nil {
			close(sp.done)
			sp.wg.Wait()
			return sp.getResponse(), sp.getReasoning(), sp.placeholderMsg, recvErr
		}
		sp.processChunk(msg)
	}
	// Signal ticker to stop
	close(sp.done)
	sp.wg.Wait()
	// Finalize
	return sp.finalize()
}

// tickerLoop periodically updates the Telegram message.
func (sp *streamProcessor) tickerLoop(ticker *time.Ticker) {
	defer sp.wg.Done()
	for {
		select {
		case <-sp.done:
			return
		case <-sp.ctx.Done():
			return
		case <-ticker.C:
			sp.updateMessage()
		}
	}
}

// processChunk handles a single streamed message chunk.
func (sp *streamProcessor) processChunk(msg *schema.Message) {
	if msg == nil {
		return
	}

	sp.mu.Lock()
	defer sp.mu.Unlock()

	if isClearStreamOutputMessage(msg) {
		sp.fullResponse.Reset()
		sp.reasoningContent.Reset()
		return
	}

	if msg.Content != "" {
		sp.fullResponse.WriteString(msg.Content)
	}
	if msg.ReasoningContent != "" {
		sp.reasoningContent.WriteString(unquoteJSONString(msg.ReasoningContent))
	}
}

// unquoteJSONString attempts to decode a JSON-encoded string value.
// The eino library's populateRCFromExtra converts json.RawMessage to string
// via string(), which preserves JSON string delimiters (quotes) and escape
// sequences. This function reverses that by JSON-unmarshalling if the value
// looks like a JSON string.
func unquoteJSONString(s string) string {
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		var unquoted string
		if err := json.Unmarshal([]byte(s), &unquoted); err == nil {
			return unquoted
		}
	}
	return s
}

// updateMessage edits the placeholder message with current content.
func (sp *streamProcessor) updateMessage() {
	sp.mu.RLock()
	text := sp.fullResponse.String()
	reason := sp.reasoningContent.String()
	sp.mu.RUnlock()

	if len(text) == 0 && len(reason) == 0 {
		return
	}
	parts := splitOutputWithReason(text, reason, sp.format)
	if shouldSuppressPartialRichEnvelope(parts.payload, sp.richEnabled) {
		delivery := resolveTelegramRichDelivery(text, reason, sp.format, sp.richEnabled)
		if delivery.ShouldSendRich {
			_ = sp.editRichPlaceholder(delivery.RichMessage, false)
		}
		return
	}

	// Find a sentence boundary for clean display
	displayText := text
	if len(sp.sentenceDelims) > 0 {
		if idx := findLastSentenceDelimiter(text, sp.sentenceDelims); idx > 0 {
			displayText = text[:idx]
		}
	}

	formatted := FormatOutputWithReason(displayText, reason, sp.format)
	if formatted == "" {
		return
	}

	_ = sp.editPlaceholder(formatted, false)
}

// finalize sends the final complete message and sets the finalized lifecycle flag.
func (sp *streamProcessor) finalize() (string, string, *tb.Message, error) {
	text := sp.getResponse()
	reason := sp.getReasoning()
	if text == "" && reason == "" {
		if sp.tc != nil {
			sp.tc.finalized.Store(true)
		}
		return "", "", sp.placeholderMsg, nil
	}
	delivery := resolveTelegramRichDelivery(text, reason, sp.format, sp.richEnabled)
	if delivery.ShouldSendRich {
		var sent *tb.Message
		var err error
		if sp.placeholderMsg != nil {
			err = sp.editRichPlaceholder(delivery.RichMessage, true)
			sent = sp.placeholderMsg
		} else {
			replyToID := 0
			if msg := sp.tbCtx.Message(); msg != nil {
				replyToID = msg.ID
			}
			sent, err = sendTelegramRichMessage(sp.telegramRaw(), sp.tbCtx.Chat().ID, replyToID, delivery.RichMessage)
		}
		if err == nil {
			if sp.tc != nil {
				sp.tc.finalized.Store(true)
			}
			if sent != nil {
				sp.placeholderMsg = sent
			}
			return delivery.VisibleText, reason, sp.placeholderMsg, nil
		}
		zap.L().Debug("chatv2: failed to send rich streaming message", zap.Error(err))
		text = delivery.VisibleText
		reason = ""
	}
	if delivery.VisibleText != "" && delivery.VisibleText != text {
		text = delivery.VisibleText
		reason = ""
	}
	formatted := FormatOutputWithReason(text, reason, sp.format)
	if err := sp.editPlaceholder(formatted, true); err != nil {
		return text, reason, sp.placeholderMsg, err
	}
	if sp.tc != nil {
		sp.tc.finalized.Store(true)
	}
	return text, reason, sp.placeholderMsg, nil
}

func (sp *streamProcessor) editRichPlaceholder(rich inputRichMessage, force bool) error {
	if sp.placeholderMsg == nil || rich.Markdown == "" {
		return nil
	}
	if sp.tc != nil {
		sp.tc.editMu.Lock()
		defer sp.tc.editMu.Unlock()
		if !force && !sp.tc.ShouldAllowEdit(sp.editInterval) {
			return nil
		}
	}
	sent, err := editTelegramRichMessage(sp.telegramRaw(), sp.placeholderMsg.Chat.ID, sp.placeholderMsg.ID, rich)
	if err != nil {
		zap.L().Debug("chatv2: failed to edit streaming rich message", zap.Error(err))
		return err
	}
	if sent != nil {
		sp.placeholderMsg = sent
	}
	if sp.tc != nil {
		sp.tc.MarkEdited()
	}
	return nil
}

func (sp *streamProcessor) telegramRaw() telegramRawCaller {
	if sp.rawCaller != nil {
		return sp.rawCaller
	}
	return sp.tbCtx.Bot()
}

// editPlaceholder edits the placeholder message with new content.
// If a TurnContext is available, uses editMu to prevent races with update_progress.
func (sp *streamProcessor) editPlaceholder(formatted string, force bool) error {
	if sp.placeholderMsg == nil || formatted == "" {
		return nil
	}

	if sp.tc != nil {
		sp.tc.editMu.Lock()
		defer sp.tc.editMu.Unlock()
		if !force && !sp.tc.ShouldAllowEdit(sp.editInterval) {
			return nil
		}
	}
	parseMode := GetParseMode(sp.format)
	_, err := util.EditMessageWithError(
		sp.placeholderMsg,
		util.RawTgText(formatted),
		&tb.SendOptions{ParseMode: parseMode},
	)
	if err != nil {
		_, err = sp.tbCtx.Bot().Edit(sp.placeholderMsg, formatted)
		if err != nil {
			zap.L().Debug("chatv2: failed to edit streaming message",
				zap.Error(err),
			)
			return err
		}
	}
	if sp.tc != nil {
		sp.tc.MarkEdited()
	}
	return nil
}

// getResponse returns the accumulated response text.
func (sp *streamProcessor) getResponse() string {
	sp.mu.RLock()
	defer sp.mu.RUnlock()
	return sp.fullResponse.String()
}

// getReasoning returns the accumulated reasoning content.
func (sp *streamProcessor) getReasoning() string {
	sp.mu.RLock()
	defer sp.mu.RUnlock()
	return sp.reasoningContent.String()
}

// NonStreamResponse sends a complete response without streaming.
// If existingMsg is provided, edits it instead of sending a new message.
// Returns the sent message, the visible text used for persistence, and any error.
func NonStreamResponse(
	tbCtx tb.Context,
	text string,
	reasoning string,
	format *config.ChatOutputFormatConfig,
	existingMsg *tb.Message,
	richEnabled bool,
) (*tb.Message, string, error) {
	delivery := resolveTelegramRichDelivery(text, reasoning, format, richEnabled)
	if delivery.ShouldSendRich {
		if existingMsg != nil {
			msg, err := editTelegramRichMessage(tbCtx.Bot(), existingMsg.Chat.ID, existingMsg.ID, delivery.RichMessage)
			if err == nil {
				return msg, delivery.VisibleText, nil
			}
			zap.L().Debug("chatv2: failed to edit rich non-stream message", zap.Error(err))
		} else {
			replyToID := 0
			if msg := tbCtx.Message(); msg != nil {
				replyToID = msg.ID
			}
			msg, err := sendTelegramRichMessage(tbCtx.Bot(), tbCtx.Chat().ID, replyToID, delivery.RichMessage)
			if err == nil {
				return msg, delivery.VisibleText, nil
			}
			zap.L().Debug("chatv2: failed to send rich non-stream message", zap.Error(err))
		}
		text = delivery.VisibleText
		reasoning = ""
	}
	if delivery.VisibleText != "" && delivery.VisibleText != text {
		text = delivery.VisibleText
		reasoning = ""
	}
	formatted := FormatOutputWithReason(text, reasoning, format)
	parseMode := GetParseMode(format)
	if existingMsg != nil {
		// Edit existing progress placeholder
		_, err := util.EditMessageWithError(
			existingMsg,
			util.RawTgText(formatted),
			&tb.SendOptions{ParseMode: parseMode},
		)
		if err != nil {
			// Fallback: edit without formatting
			_, err = tbCtx.Bot().Edit(existingMsg, text)
			if err != nil {
				zap.L().Debug("chatv2: failed to edit non-stream message", zap.Error(err))
				return existingMsg, text, err
			}
		}
		return existingMsg, text, nil
	}

	// Send new message (original behavior)
	sent, err := util.SendMessageWithError(
		tbCtx.Chat(),
		util.RawTgText(formatted),
		&tb.SendOptions{
			ParseMode: parseMode,
			ReplyTo:   tbCtx.Message(),
		},
	)
	if err != nil {
		// Fallback: send without formatting
		sent, err = tbCtx.Bot().Send(tbCtx.Chat(), text, &tb.SendOptions{
			ReplyTo: tbCtx.Message(),
		})
	}
	return sent, text, err
}
