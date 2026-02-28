//go:build !386 && !arm

package chatv2

import (
	"context"
	"text/template"

	"csust-got/chat"
	"csust-got/config"

	"github.com/cloudwego/eino/flow/agent/react"
	tb "gopkg.in/telebot.v3"
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

// CompiledChat is a pre-compiled chat configuration ready for concurrent reuse.
// Created once at init time, used for every incoming request matching this chat config.
type CompiledChat struct {
	Name           string
	Config         *config.ChatConfigSingle
	Agent          *react.Agent
	SystemTemplate *template.Template
	PromptTemplate *template.Template
}

// promptData is the template rendering data, compatible with existing chat prompt templates.
type promptData struct {
	DateTime        string
	Input           string
	ContextMessages []*chat.ContextMessage
	ContextText     string
	ContextXml      string
	ReplyToXml      string
	BotUsername     string
}
