//go:build !386 && !arm

package chatv2

import (
	"context"
	"csust-got/chat"
	"csust-got/config"
	model "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/flow/agent/react"
	tb "gopkg.in/telebot.v3"
	"sync"
	"sync/atomic"
	"text/template"
)

// turnContextKey is the Go context key for per-request runtime data.
type turnContextKey struct{}

// TurnContext holds per-request runtime data passed through Go context.
// Tools and subagents access this to interact with Telegram, Redis, etc.
type TurnContext struct {
	Bot     *tb.Bot
	Message *tb.Message
	ChatID  int64
	Config  *config.ChatConfigSingle
	Trigger *config.ChatTrigger
	BotUser *tb.User
	// Progress tracking — used by update_progress tool and streaming handlers.
	// editMu serializes ALL edits to progressMsg to avoid Telegram race conditions.
	editMu           sync.Mutex
	progressMsg      *tb.Message                // Placeholder message for progress/streaming
	progressModel    model.ToolCallingChatModel // Lazily-built small model for summarization
	progressOnce     sync.Once                  // Ensures progressModel is built once
	progressModelErr error                      // Error from building progressModel
	streamingStarted atomic.Bool                // Set true when streaming/final output begins
	finalized        atomic.Bool                // Set true after final response sent
}

// WithTurnContext stores TurnContext in a Go context.
func WithTurnContext(ctx context.Context, tc *TurnContext) context.Context {
	return context.WithValue(ctx, turnContextKey{}, tc)
}

// GetTurnContext retrieves TurnContext from a Go context.
func GetTurnContext(ctx context.Context) *TurnContext {
	if v, ok := ctx.Value(turnContextKey{}).(*TurnContext); ok {
		return v
	}
	return nil
}

// SetProgressMsg atomically sets the Telegram placeholder message for progress updates.
func (tc *TurnContext) SetProgressMsg(msg *tb.Message) {
	tc.editMu.Lock()
	defer tc.editMu.Unlock()
	tc.progressMsg = msg
}

// GetProgressMsg atomically retrieves the current progress placeholder message.
func (tc *TurnContext) GetProgressMsg() *tb.Message {
	tc.editMu.Lock()
	defer tc.editMu.Unlock()
	return tc.progressMsg
}

// GetOrBuildProgressModel lazily builds and returns the progress summarization model.
// Returns (nil, nil) if no progress summary model is configured.
func (tc *TurnContext) GetOrBuildProgressModel(ctx context.Context) (model.ToolCallingChatModel, error) {
	psCfg := tc.Config.Format.ProgressSummary
	if psCfg == nil || psCfg.Model == nil {
		return nil, nil
	}
	tc.progressOnce.Do(func() {
		tc.progressModel, tc.progressModelErr = buildModel(ctx, psCfg.Model)
	})
	return tc.progressModel, tc.progressModelErr
}

// CompiledChat is a pre-compiled chat configuration ready for concurrent reuse.
// Created once at init time, used for every incoming request matching this chat config.
type CompiledChat struct {
	Name           string
	Config         *config.ChatConfigSingle
	Agent          *react.Agent
	SystemTemplate *template.Template
	PromptTemplate *template.Template
}

// RichHistory keeps both the rendered text context and the underlying Telegram
// messages so chatv2 can recover media attachments for multimodal input.
type RichHistory struct {
	ContextMessages []*chat.ContextMessage
	FullMessages    []*tb.Message
}

// PromptData is the template rendering data exposed to chatv2 prompt templates.
type PromptData struct {
	DateTime        string
	CurrentDateCN   string
	Input           string
	ContextMessages []*chat.ContextMessage
	ContextText     string
	ContextXml      string
	ReplyToXml      string
	BotUsername     string
}
