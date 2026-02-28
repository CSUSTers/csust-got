package chatv2

import (
	"csust-got/config"

	"go.uber.org/zap"
	tb "gopkg.in/telebot.v3"
)

// Filter is the interface for message filters in chatv2.
// Filters can modify or block messages at various stages.
type Filter interface {
	// Name returns the filter name for logging.
	Name() string
	// Check returns true if the message should be processed, false to block.
	Check(tbCtx tb.Context, chatCfg *config.ChatConfigSingle) bool
}

// whitelistFilter checks if the user/chat is allowed to use this chat config.
type whitelistFilter struct{}

func (f *whitelistFilter) Name() string { return "whitelist" }

func (f *whitelistFilter) Check(tbCtx tb.Context, chatCfg *config.ChatConfigSingle) bool {
	msg := tbCtx.Message()
	if msg == nil {
		return false
	}

	// Check each filter setting
	for _, filterCfg := range chatCfg.Filters.Filters {
		if filterCfg.Type != "whitelist" {
			continue
		}

		// If whitelist is configured, check membership
		for _, allowedID := range filterCfg.Whitelist {
			if msg.Chat.ID == allowedID || (msg.Sender != nil && msg.Sender.ID == allowedID) {
				return true
			}
		}

		// Whitelist configured but user not in it
		var userID int64
		if msg.Sender != nil {
			userID = msg.Sender.ID
		}
		zap.L().Debug("chatv2/filter: user not in whitelist",
			zap.Int64("user_id", userID),
			zap.String("chat", chatCfg.Name),
		)
		return false
	}

	// No whitelist filter configured = allow all
	return true
}

// ProcessFilters runs all configured filters on the message.
// Returns true if the message should be processed, false to block.
func ProcessFilters(tbCtx tb.Context, chatCfg *config.ChatConfigSingle) bool {
	if len(chatCfg.Filters.Filters) == 0 {
		return true
	}

	filters := buildFilters(chatCfg)
	for _, f := range filters {
		if !f.Check(tbCtx, chatCfg) {
			zap.L().Debug("chatv2/filter: blocked by filter",
				zap.String("filter", f.Name()),
				zap.String("chat", chatCfg.Name),
			)
			return false
		}
	}

	return true
}

// buildFilters creates filter instances from config.
func buildFilters(chatCfg *config.ChatConfigSingle) []Filter {
	var filters []Filter
	seen := make(map[string]bool)

	for _, filterCfg := range chatCfg.Filters.Filters {
		if seen[filterCfg.Type] {
			continue
		}
		seen[filterCfg.Type] = true

		switch filterCfg.Type {
		case "whitelist":
			filters = append(filters, &whitelistFilter{})
		default:
			zap.L().Warn("chatv2/filter: unknown filter type",
				zap.String("type", filterCfg.Type),
			)
		}
	}

	return filters
}
