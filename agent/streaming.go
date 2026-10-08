package agentv3

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	format *config.AgentOutputConfig,
	existingMsg *tb.Message,
	richEnabled bool,
) (response string, reasoning string, sentMsg *tb.Message, err error) {
	result, err := streamToTelegramWithDelivery(ctx, tbCtx, reader, format, existingMsg, richEnabled)
	return result.response, result.reasoning, result.sent, err
}

type telegramResponseResult struct {
	response  string
	reasoning string
	sent      *tb.Message
	// delivered is the last message proven to carry final content; deliveredAll lists every final message in order.
	delivered    *tb.Message
	deliveredAll []*tb.Message
}

func streamToTelegramWithDelivery(
	ctx context.Context,
	tbCtx tb.Context,
	reader *schema.StreamReader[*schema.Message],
	format *config.AgentOutputConfig,
	existingMsg *tb.Message,
	richEnabled bool,
) (telegramResponseResult, error) {
	tc := GetTurnContext(ctx) // may be nil outside agent handling
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

	response, reasoning, sent, err := sp.process(existingMsg)
	return telegramResponseResult{response: response, reasoning: reasoning, sent: sent, delivered: sp.deliveredMsg, deliveredAll: sp.deliveredMsgs}, err
}

// telegramTextSender is the subset of *tb.Bot used to deliver text; tests substitute fakes.
type telegramTextSender interface {
	Send(to tb.Recipient, what any, opts ...any) (*tb.Message, error)
	Edit(msg tb.Editable, what any, opts ...any) (*tb.Message, error)
}

// streamProcessor manages the streaming output lifecycle.
type streamProcessor struct {
	ctx            context.Context
	tbCtx          tb.Context
	reader         *schema.StreamReader[*schema.Message]
	format         *config.AgentOutputConfig
	richEnabled    bool
	rawCaller      telegramRawCaller
	sender         telegramTextSender
	now            func() time.Time
	sentenceDelims []string
	editInterval   time.Duration

	// State protected by mutex
	mu               sync.RWMutex
	fullResponse     strings.Builder
	reasoningContent strings.Builder
	placeholderMsg   *tb.Message
	deliveredMsg     *tb.Message
	deliveredMsgs    []*tb.Message
	tc               *TurnContext // For editMu locking and lifecycle flags
	deleteOnError    bool

	// Edit dedupe and flood backoff state, protected by editStateMu
	editStateMu       sync.Mutex
	lastSentFormatted string
	lastEditAttempt   time.Time
	floodNotBefore    time.Time
	floodBackoff      time.Duration

	// Ticker control
	done chan struct{}
	wg   sync.WaitGroup
}

const (
	defaultEditInterval         = 3 * time.Second
	streamingEditBackoffCap     = 10 * time.Second
	telegramFloodRetryCap       = 60 * time.Second
	streamControlClearOutputKey = "csust-got:clear-stream-output"
)

var errTelegramDeliveryNoChat = errors.New("agentv3: no target chat for delivery")

var telegramFloodSleep = func(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

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

func getEditInterval(format *config.AgentOutputConfig) time.Duration {
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
			if sp.shouldDeletePlaceholderOnStreamError() {
				sp.deletePlaceholderAfterClearedStreamError()
			}
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
		sp.deleteOnError = sp.placeholderMsg != nil
		return
	}

	if msg.Content != "" {
		sp.fullResponse.WriteString(msg.Content)
	}
	if msg.ReasoningContent != "" {
		sp.reasoningContent.WriteString(unquoteJSONString(msg.ReasoningContent))
	}
}

func (sp *streamProcessor) shouldDeletePlaceholderOnStreamError() bool {
	sp.mu.RLock()
	defer sp.mu.RUnlock()
	return sp.deleteOnError
}

func (sp *streamProcessor) deletePlaceholderAfterClearedStreamError() {
	sp.mu.Lock()
	sp.fullResponse.Reset()
	sp.reasoningContent.Reset()
	sp.deleteOnError = false
	sp.mu.Unlock()
	if sp.placeholderMsg == nil || sp.placeholderMsg.Chat == nil {
		sp.placeholderMsg = nil
		return
	}
	if bot := sp.tbCtx.Bot(); bot != nil {
		if err := bot.Delete(sp.placeholderMsg); err != nil {
			zap.L().Debug("agentv3: failed to delete cleared streaming placeholder", zap.Error(err))
		}
	}
	sp.placeholderMsg = nil
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
		return
	}
	if sp.richEnabled {
		delivery := resolveTelegramRichDelivery(text, reason, sp.format, true, sp.richAuthorized())
		if delivery.ShouldSendRich {
			return
		}
	}

	// Find a sentence boundary for clean display
	displayText := text
	if len(sp.sentenceDelims) > 0 {
		if idx := findLastSentenceDelimiter(text, sp.sentenceDelims); idx > 0 {
			displayText = text[:idx]
		}
	}

	preview := tailTelegramPreview(displayText, reason, sp.format, telegramMessageLimit)
	if preview.formatted == "" {
		return
	}

	_, _ = sp.editPlaceholder(preview, false)
}

// finalize sends the final complete message and sets the finalized lifecycle flag.
func (sp *streamProcessor) finalize() (string, string, *tb.Message, error) {
	sp.deliveredMsg = nil
	sp.deliveredMsgs = nil
	text := sp.getResponse()
	reason := sp.getReasoning()
	if text == "" && reason == "" {
		if sp.tc != nil {
			sp.tc.finalized.Store(true)
		}
		return "", "", sp.placeholderMsg, nil
	}
	var richErr error
	delivery := resolveTelegramRichDelivery(text, reason, sp.format, sp.richEnabled, sp.richAuthorized())
	if delivery.ShouldSendRich {
		replyToID := 0
		if msg := sp.tbCtx.Message(); msg != nil {
			replyToID = msg.ID
		}
		sent, err := sendTelegramRichMessage(sp.telegramRaw(), sp.targetChatID(), replyToID, delivery.RichMessage)
		if err == nil {
			sp.deliveredMsg, _ = telegramDeliveryProof(sent, nil, err)
			if sp.deliveredMsg != nil {
				sp.deliveredMsgs = []*tb.Message{sp.deliveredMsg}
			}
			if sp.tc != nil {
				sp.tc.finalized.Store(true)
			}
			sp.deletePlaceholderAfterRichSend(sent)
			if sent != nil {
				sp.placeholderMsg = sent
			}
			return delivery.VisibleText, reason, sp.placeholderMsg, nil
		}
		zap.L().Warn("agentv3: failed to send rich streaming message, falling back to plain text", zap.Error(err))
		richErr = err
		text, reason = delivery.VisibleText, ""
	} else if delivery.VisibleText != "" && delivery.VisibleText != text {
		text = delivery.VisibleText
		reason = ""
	}
	if err := sp.deliverPlainFinal(text, reason); err != nil {
		if richErr != nil {
			err = fmt.Errorf("%w; plain fallback: %w", richErr, err)
		}
		if sp.shouldDeletePlaceholderOnStreamError() {
			sp.deletePlaceholderAfterClearedStreamError()
			return "", "", nil, err
		}
		return text, reason, sp.placeholderMsg, err
	}
	if sp.tc != nil {
		sp.tc.finalized.Store(true)
	}
	return text, reason, sp.finalSent(), nil
}

// deliverPlainFinal edits the placeholder with the first chunk of the final text and sends any further chunks as replies.
func (sp *streamProcessor) deliverPlainFinal(text, reason string) error {
	chunks := formatTelegramChunks(text, reason, sp.format, telegramMessageLimit)
	if len(chunks) == 0 || sp.placeholderMsg == nil {
		return nil
	}
	delivered, err := sp.deliverer().deliverChunks(chunks, sp.placeholderMsg, func(chunk telegramChunk) (*tb.Message, error) {
		return sp.editPlaceholder(chunk, true)
	})
	if err != nil {
		return err
	}
	sp.deliveredMsgs = delivered
	if len(delivered) > 0 {
		sp.deliveredMsg = delivered[len(delivered)-1]
	}
	return nil
}

func (sp *streamProcessor) finalSent() *tb.Message {
	if len(sp.deliveredMsgs) > 1 {
		return sp.deliveredMsgs[len(sp.deliveredMsgs)-1]
	}
	return sp.placeholderMsg
}

func (sp *streamProcessor) telegramRaw() telegramRawCaller {
	if sp.rawCaller != nil {
		return sp.rawCaller
	}
	return sp.tbCtx.Bot()
}

func (sp *streamProcessor) textSender() telegramTextSender {
	if sp.sender != nil {
		return sp.sender
	}
	return sp.tbCtx.Bot()
}

func (sp *streamProcessor) clock() time.Time {
	if sp.now != nil {
		return sp.now()
	}
	return time.Now()
}

func (sp *streamProcessor) deliverer() telegramDeliverer {
	return telegramDeliverer{ctx: sp.ctx, sender: sp.textSender(), format: sp.format, chat: sp.targetChat()}
}

func (sp *streamProcessor) richAuthorized() bool {
	return sp.richEnabled && sp.tc != nil && sp.tc.richMessageSkillLoadedForFinal()
}

func (sp *streamProcessor) targetChat() *tb.Chat {
	if sp.placeholderMsg != nil && sp.placeholderMsg.Chat != nil && sp.placeholderMsg.Chat.ID != 0 {
		return sp.placeholderMsg.Chat
	}
	return sp.tbCtx.Chat()
}

func (sp *streamProcessor) targetChatID() int64 {
	if chat := sp.targetChat(); chat != nil {
		return chat.ID
	}
	return 0
}

func (sp *streamProcessor) deletePlaceholderAfterRichSend(sent *tb.Message) {
	if sp.placeholderMsg == nil || sp.placeholderMsg.Chat == nil {
		return
	}
	if sent != nil && sent.Chat != nil && sent.Chat.ID == sp.placeholderMsg.Chat.ID && sent.ID == sp.placeholderMsg.ID {
		return
	}
	if bot := sp.tbCtx.Bot(); bot != nil {
		if err := bot.Delete(sp.placeholderMsg); err != nil {
			zap.L().Debug("agentv3: failed to delete rich placeholder", zap.Error(err))
		}
	}
}

// editPlaceholder edits the placeholder message with new content and returns the delivery proof.
// Unchanged text is not re-sent, flood errors pause periodic edits and widen the edit interval,
// and a final (force) edit waits out one flood window before giving up.
// If a TurnContext is available, uses editMu to prevent races with update_progress.
func (sp *streamProcessor) editPlaceholder(chunk telegramChunk, force bool) (*tb.Message, error) {
	if sp.placeholderMsg == nil || chunk.formatted == "" {
		return nil, nil
	}

	if sp.tc != nil {
		sp.tc.editMu.Lock()
		defer sp.tc.editMu.Unlock()
	}
	sp.editStateMu.Lock()
	defer sp.editStateMu.Unlock()

	if chunk.formatted == sp.lastSentFormatted {
		return sp.placeholderMsg, nil
	}
	now := sp.clock()
	if !force && !sp.editGateOpen(now) {
		return nil, nil
	}
	sp.lastEditAttempt = now
	if sp.tc != nil {
		sp.tc.MarkEdited()
	}

	proof, err := sp.deliverer().editChunk(sp.placeholderMsg, chunk, force)
	if err != nil {
		if wait, flooded := util.FloodRetryAfter(err); flooded {
			sp.recordFlood(now, wait)
			zap.L().Warn("agentv3: telegram flood limit hit while editing streaming message",
				zap.Duration("retry_after", wait),
				zap.Duration("edit_backoff", sp.floodBackoff),
			)
		} else {
			zap.L().Debug("agentv3: failed to edit streaming message", zap.Error(err))
		}
		return nil, err
	}
	sp.lastSentFormatted = chunk.formatted
	sp.floodNotBefore = time.Time{}
	sp.floodBackoff = 0
	sp.mu.Lock()
	sp.deleteOnError = false
	sp.mu.Unlock()
	return proof, nil
}

func (sp *streamProcessor) editGateOpen(now time.Time) bool {
	if now.Before(sp.floodNotBefore) {
		return false
	}
	interval := sp.editInterval
	if sp.floodBackoff > interval {
		interval = sp.floodBackoff
	}
	if sp.tc != nil {
		return sp.tc.ShouldAllowEdit(interval)
	}
	if sp.floodBackoff <= 0 || sp.lastEditAttempt.IsZero() {
		return true
	}
	return now.Sub(sp.lastEditAttempt) >= interval
}

func (sp *streamProcessor) recordFlood(now time.Time, wait time.Duration) {
	sp.floodNotBefore = now.Add(wait)
	base := max(sp.floodBackoff, sp.editInterval, time.Second)
	sp.floodBackoff = min(base*2, streamingEditBackoffCap)
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

// telegramDeliverer sends final text with flood retry, raw-text fallback and chunking.
type telegramDeliverer struct {
	ctx    context.Context
	sender telegramTextSender
	format *config.AgentOutputConfig
	chat   *tb.Chat
}

func (d telegramDeliverer) context() context.Context {
	if d.ctx != nil {
		return d.ctx
	}
	return context.Background()
}

// withFloodRetry runs op and, when Telegram answers with a flood error and waitOnFlood is set,
// waits out the requested window (capped) once before retrying.
func (d telegramDeliverer) withFloodRetry(waitOnFlood bool, op func() (*tb.Message, error)) (*tb.Message, error) {
	sent, err := op()
	wait, flooded := util.FloodRetryAfter(err)
	if !flooded || !waitOnFlood {
		return sent, err
	}
	zap.L().Warn("agentv3: telegram flood limit hit during final delivery, waiting before retry", zap.Duration("retry_after", wait))
	if sleepErr := telegramFloodSleep(d.context(), min(wait, telegramFloodRetryCap)); sleepErr != nil {
		return nil, errors.Join(err, sleepErr)
	}
	return op()
}

// editChunk edits target with the formatted chunk, falling back to the raw text when formatting is rejected.
func (d telegramDeliverer) editChunk(target *tb.Message, chunk telegramChunk, waitOnFlood bool) (*tb.Message, error) {
	edited, err := d.withFloodRetry(waitOnFlood, func() (*tb.Message, error) {
		return d.sender.Edit(target, chunk.formatted, &tb.SendOptions{ParseMode: GetParseMode(d.format)})
	})
	proof, err := telegramDeliveryProof(edited, target, err)
	if err == nil {
		return proof, nil
	}
	if _, flooded := util.FloodRetryAfter(err); flooded || chunk.raw == "" {
		return nil, err
	}
	zap.L().Debug("agentv3: formatted edit rejected, retrying with raw text", zap.Error(err))
	edited, err = d.withFloodRetry(waitOnFlood, func() (*tb.Message, error) {
		return d.sender.Edit(target, chunk.raw, &tb.SendOptions{ParseMode: tb.ModeDefault})
	})
	return telegramDeliveryProof(edited, target, err)
}

// sendChunk sends the formatted chunk as a reply, falling back to the raw text when formatting is rejected.
func (d telegramDeliverer) sendChunk(replyTo *tb.Message, chunk telegramChunk, waitOnFlood bool) (*tb.Message, error) {
	if d.chat == nil {
		return nil, errTelegramDeliveryNoChat
	}
	opts := func(mode tb.ParseMode) *tb.SendOptions {
		return &tb.SendOptions{ParseMode: mode, ReplyTo: replyTo, AllowWithoutReply: replyTo != nil}
	}
	sent, err := d.withFloodRetry(waitOnFlood, func() (*tb.Message, error) {
		return d.sender.Send(d.chat, chunk.formatted, opts(GetParseMode(d.format)))
	})
	if err == nil {
		return telegramDeliveryProof(sent, nil, nil)
	}
	if _, flooded := util.FloodRetryAfter(err); flooded || chunk.raw == "" {
		return nil, err
	}
	zap.L().Debug("agentv3: formatted send rejected, retrying with raw text", zap.Error(err))
	sent, err = d.withFloodRetry(waitOnFlood, func() (*tb.Message, error) {
		return d.sender.Send(d.chat, chunk.raw, opts(tb.ModeDefault))
	})
	return telegramDeliveryProof(sent, nil, err)
}

// deliverChunks delivers the first chunk through first and every later chunk as a reply to the previous one.
// It returns the messages proven delivered so far together with the first error; an accepted call that
// yields no usable message is not an error, it simply contributes no proof.
func (d telegramDeliverer) deliverChunks(chunks []telegramChunk, replyTo *tb.Message, first func(telegramChunk) (*tb.Message, error)) ([]*tb.Message, error) {
	delivered := make([]*tb.Message, 0, len(chunks))
	for i, chunk := range chunks {
		var msg *tb.Message
		var err error
		if i == 0 {
			msg, err = first(chunk)
		} else {
			msg, err = d.sendChunk(replyTo, chunk, true)
		}
		if err != nil {
			return delivered, err
		}
		if msg != nil {
			delivered = append(delivered, msg)
			replyTo = msg
		}
	}
	return delivered, nil
}

// NonStreamResponse sends a complete response without streaming.
// If existingMsg is provided, plain responses edit it while rich responses are sent as new messages.
// Returns the sent message, the visible text used for persistence, and any error.
func NonStreamResponse(
	tbCtx tb.Context,
	text string,
	reasoning string,
	format *config.AgentOutputConfig,
	existingMsg *tb.Message,
	richEnabled bool,
	richAuthorized bool,
) (*tb.Message, string, error) {
	return nonStreamResponseWithCaller(tbCtx.Bot(), tbCtx, text, reasoning, format, existingMsg, richEnabled, richAuthorized)
}

func nonStreamResponseWithCaller(
	raw telegramRawCaller,
	tbCtx tb.Context,
	text string,
	reasoning string,
	format *config.AgentOutputConfig,
	existingMsg *tb.Message,
	richEnabled bool,
	richAuthorized bool,
) (*tb.Message, string, error) {
	result, err := nonStreamResponseWithDelivery(context.Background(), raw, tbCtx, text, reasoning, format, existingMsg, richEnabled, richAuthorized)
	return result.sent, result.response, err
}

func nonStreamResponseWithDelivery(
	ctx context.Context,
	raw telegramRawCaller,
	tbCtx tb.Context,
	text string,
	reasoning string,
	format *config.AgentOutputConfig,
	existingMsg *tb.Message,
	richEnabled bool,
	richAuthorized bool,
) (telegramResponseResult, error) {
	result := telegramResponseResult{sent: existingMsg, response: text}
	var richErr error
	delivery := resolveTelegramRichDelivery(text, reasoning, format, richEnabled, richAuthorized)
	if delivery.ShouldSendRich {
		replyToID := 0
		if msg := tbCtx.Message(); msg != nil {
			replyToID = msg.ID
		}
		chatID := int64(0)
		if existingMsg != nil && existingMsg.Chat != nil {
			chatID = existingMsg.Chat.ID
		} else if chat := tbCtx.Chat(); chat != nil {
			chatID = chat.ID
		}
		msg, err := sendTelegramRichMessage(raw, chatID, replyToID, delivery.RichMessage)
		result.response = delivery.VisibleText
		if err == nil {
			deleteExistingPlaceholderAfterRichSend(tbCtx, existingMsg, msg)
			result.sent = msg
			result.delivered, _ = telegramDeliveryProof(msg, nil, err)
			if result.delivered != nil {
				result.deliveredAll = []*tb.Message{result.delivered}
			}
			return result, nil
		}
		zap.L().Warn("agentv3: failed to send rich non-stream message, falling back to plain text", zap.Error(err))
		richErr = err
		text, reasoning = delivery.VisibleText, ""
	} else if delivery.VisibleText != "" && delivery.VisibleText != text {
		text = delivery.VisibleText
		reasoning = ""
	}
	result.response = text
	chunks := formatTelegramChunks(text, reasoning, format, telegramMessageLimit)
	if len(chunks) == 0 {
		return result, richErr
	}

	sender := telegramTextSender(tbCtx.Bot())
	if s, ok := raw.(telegramTextSender); ok {
		sender = s
	}
	var chat *tb.Chat
	if existingMsg != nil && existingMsg.Chat != nil {
		chat = existingMsg.Chat
	} else {
		chat = tbCtx.Chat()
	}
	d := telegramDeliverer{ctx: ctx, sender: sender, format: format, chat: chat}
	replyTo := existingMsg
	if replyTo == nil {
		replyTo = tbCtx.Message()
	}
	delivered, err := d.deliverChunks(chunks, replyTo, func(chunk telegramChunk) (*tb.Message, error) {
		if existingMsg != nil {
			return d.editChunk(existingMsg, chunk, true)
		}
		return d.sendChunk(tbCtx.Message(), chunk, true)
	})
	if err != nil {
		if richErr != nil {
			err = fmt.Errorf("%w; plain fallback: %w", richErr, err)
		}
		zap.L().Debug("agentv3: failed to deliver non-stream message", zap.Error(err))
		return result, err
	}
	if len(delivered) == 0 {
		return result, nil
	}
	result.deliveredAll = delivered
	result.delivered = delivered[len(delivered)-1]
	if existingMsg == nil || len(delivered) > 1 {
		result.sent = result.delivered
	}
	return result, nil
}

func telegramDeliveryProof(sent, editTarget *tb.Message, err error) (*tb.Message, error) {
	if editTarget != nil && (errors.Is(err, tb.ErrMessageNotModified) || errors.Is(err, tb.ErrSameMessageContent)) {
		// Telegram confirms the requested final content is already on this message.
		sent, err = editTarget, nil
	}
	if err != nil || sent == nil || sent.ID <= 0 {
		return nil, err
	}
	return sent, nil
}

func deleteExistingPlaceholderAfterRichSend(tbCtx tb.Context, existingMsg, sent *tb.Message) {
	if existingMsg == nil || existingMsg.Chat == nil {
		return
	}
	if sent != nil && sent.Chat != nil && sent.Chat.ID == existingMsg.Chat.ID && sent.ID == existingMsg.ID {
		return
	}
	if bot := tbCtx.Bot(); bot != nil {
		if err := bot.Delete(existingMsg); err != nil {
			zap.L().Debug("agentv3: failed to delete rich placeholder", zap.Error(err))
		}
	}
}
