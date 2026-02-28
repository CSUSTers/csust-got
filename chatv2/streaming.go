package chatv2

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"time"

	"csust-got/config"

	"github.com/cloudwego/eino/schema"
	"go.uber.org/zap"
	tb "gopkg.in/telebot.v3"
)

// StreamToTelegram reads from an eino StreamReader and streams the output to a Telegram message.
// It sends a placeholder message first, then periodically edits it with accumulated content.
// Returns the final response text, reasoning content, and any error.
func StreamToTelegram(
	ctx context.Context,
	tbCtx tb.Context,
	reader *schema.StreamReader[*schema.Message],
	format *config.ChatOutputFormatConfig,
	placeholder string,
) (response string, reasoning string, sentMsg *tb.Message, err error) {
	sp := &streamProcessor{
		ctx:            ctx,
		tbCtx:          tbCtx,
		reader:         reader,
		format:         format,
		sentenceDelims: config.BotConfig.SentenceDelimiters,
		editInterval:   getEditInterval(format),
		done:           make(chan struct{}),
		tickerDone:     make(chan struct{}),
	}

	return sp.process(placeholder)
}

// streamProcessor manages the streaming output lifecycle.
type streamProcessor struct {
	ctx            context.Context
	tbCtx          tb.Context
	reader         *schema.StreamReader[*schema.Message]
	format         *config.ChatOutputFormatConfig
	sentenceDelims []string
	editInterval   time.Duration

	// State protected by mutex
	mu               sync.RWMutex
	fullResponse     strings.Builder
	reasoningContent strings.Builder
	placeholderMsg   *tb.Message

	// Ticker control
	done       chan struct{}
	tickerDone chan struct{}
}

func getEditInterval(format *config.ChatOutputFormatConfig) time.Duration {
	d := format.GetEditInterval()
	if d <= 0 {
		d = time.Second
	}
	return d
}

// process is the main streaming loop.
func (sp *streamProcessor) process(placeholder string) (string, string, *tb.Message, error) {
	// Send placeholder message
	parseMode := GetParseMode(sp.format)
	sent, err := sp.tbCtx.Bot().Send(
		sp.tbCtx.Chat(),
		placeholder,
		&tb.SendOptions{
			ParseMode: parseMode,
			ReplyTo:   sp.tbCtx.Message(),
		},
	)
	if err != nil {
		return "", "", nil, err
	}
	sp.placeholderMsg = sent

	// Start periodic update ticker
	ticker := time.NewTicker(sp.editInterval)
	defer ticker.Stop()

	go sp.tickerLoop(ticker)

	// Read from eino StreamReader
	for {
		msg, recvErr := sp.reader.Recv()
		if errors.Is(recvErr, io.EOF) {
			break
		}
		if recvErr != nil {
			close(sp.done)
			<-sp.tickerDone
			return sp.getResponse(), sp.getReasoning(), sp.placeholderMsg, recvErr
		}

		sp.processChunk(msg)
	}

	// Signal ticker to stop and wait for it to exit
	close(sp.done)
	<-sp.tickerDone

	// Finalize
	return sp.finalize()
}

// tickerLoop periodically updates the Telegram message.
func (sp *streamProcessor) tickerLoop(ticker *time.Ticker) {
	defer close(sp.tickerDone)
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

	if msg.Content != "" {
		sp.fullResponse.WriteString(msg.Content)
	}
	if msg.ReasoningContent != "" {
		sp.reasoningContent.WriteString(msg.ReasoningContent)
	}
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

	sp.editPlaceholder(formatted)
}

// finalize sends the final complete message.
func (sp *streamProcessor) finalize() (string, string, *tb.Message, error) {
	text := sp.getResponse()
	reason := sp.getReasoning()
	if text == "" && reason == "" {
		return "", "", sp.placeholderMsg, nil
	}
	formatted := FormatOutputWithReason(text, reason, sp.format)
	sp.editPlaceholder(formatted)
	return text, reason, sp.placeholderMsg, nil
}

// editPlaceholder edits the placeholder message with new content.
func (sp *streamProcessor) editPlaceholder(formatted string) {
	if sp.placeholderMsg == nil || formatted == "" {
		return
	}

	parseMode := GetParseMode(sp.format)
	_, err := sp.tbCtx.Bot().Edit(
		sp.placeholderMsg,
		formatted,
		&tb.SendOptions{ParseMode: parseMode},
	)
	if err != nil {
		// Fallback: try without parse mode
		_, err = sp.tbCtx.Bot().Edit(sp.placeholderMsg, formatted)
		if err != nil {
			zap.L().Debug("chatv2: failed to edit streaming message",
				zap.Error(err),
			)
		}
	}
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
// Used when stream_output is disabled.
func NonStreamResponse(
	tbCtx tb.Context,
	text string,
	reasoning string,
	format *config.ChatOutputFormatConfig,
) (*tb.Message, error) {
	formatted := FormatOutputWithReason(text, reasoning, format)
	parseMode := GetParseMode(format)

	sent, err := tbCtx.Bot().Send(
		tbCtx.Chat(),
		formatted,
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


	return sent, err
}
