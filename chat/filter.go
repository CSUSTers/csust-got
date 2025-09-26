package chat

import (
	"csust-got/config"
	tb "gopkg.in/telebot.v3"
)

// FilterResult represents the result of a filter operation
type FilterResult int

const (
	// FilterAllow indicates that the message should be processed
	FilterAllow FilterResult = iota
	// FilterDeny indicates that the message should be rejected
	FilterDeny
)

// Filter interface defines the contract for chat filters
type Filter interface {
	// ProcessIncoming processes an incoming message
	// Returns FilterAllow to continue processing or FilterDeny to reject the message
	ProcessIncoming(ctx tb.Context, chatConfig *config.ChatConfigSingle) FilterResult

	// ProcessOutgoing processes an outgoing message
	// Can modify the response before it's sent
	ProcessOutgoing(response string, ctx tb.Context, chatConfig *config.ChatConfigSingle) string

	// ProcessPromptData processes the prompt data before template execution
	// Can modify the prompt data before it's used in templates
	ProcessPromptData(data *promptData, ctx tb.Context, chatConfig *config.ChatConfigSingle) *promptData
}

// whitelistFilter implements Filter for whitelist-based access control
type whitelistFilter struct {
	whitelist map[int64]bool
}

// newWhitelistFilter creates a new whitelist filter
func newWhitelistFilter(filterConfig *config.ChatFilterConfig) *whitelistFilter {
	wl := make(map[int64]bool)
	for _, id := range filterConfig.Whitelist {
		wl[id] = true
	}
	return &whitelistFilter{whitelist: wl}
}

// ProcessIncoming checks if the chat ID is in the whitelist
func (w *whitelistFilter) ProcessIncoming(ctx tb.Context, chatConfig *config.ChatConfigSingle) FilterResult {
	chatID := ctx.Chat().ID
	if w.whitelist[chatID] {
		return FilterAllow
	}

	// Also check sender ID for private chats
	senderID := ctx.Sender().ID
	if w.whitelist[senderID] {
		return FilterAllow
	}

	return FilterDeny
}

// ProcessOutgoing does not modify outgoing messages for whitelist filter
func (w *whitelistFilter) ProcessOutgoing(response string, ctx tb.Context, chatConfig *config.ChatConfigSingle) string {
	return response
}

// ProcessPromptData does not modify prompt data for whitelist filter
func (w *whitelistFilter) ProcessPromptData(data *promptData, ctx tb.Context, chatConfig *config.ChatConfigSingle) *promptData {
	return data
}

// createFilter creates a filter based on its configuration
func createFilter(filterConfig *config.ChatFilterConfig) Filter {
	switch filterConfig.Type {
	case "whitelist":
		return newWhitelistFilter(filterConfig)
	default:
		return nil
	}
}

// ProcessFilters executes all filters in order for an incoming message
func ProcessFilters(ctx tb.Context, chatConfig *config.ChatConfigSingle) FilterResult {
	for _, filterConfig := range chatConfig.Filters.Filters {
		filter := createFilter(&filterConfig)
		if filter == nil {
			continue
		}

		result := filter.ProcessIncoming(ctx, chatConfig)
		if result == FilterDeny {
			return FilterDeny
		}

	}

	return FilterAllow
}
