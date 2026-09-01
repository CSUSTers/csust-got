//go:build !386 && !arm

package agentv3

import (
	"csust-got/config"

	"go.uber.org/zap"
	tb "gopkg.in/telebot.v3"
)

const filterTypeWhitelist = "whitelist"

// Filter is the interface for message filters in agent v3.
// Filters can modify or block messages at various stages.
type Filter interface {
	// Name returns the filter name for logging.
	Name() string
	// Check returns true if the message should be processed, false to block.
	Check(tbCtx tb.Context, chatCfg *config.AgentConfig) bool
}

// whitelistFilter checks if the user/chat is allowed to use this chat config.
type whitelistFilter struct{}

func (f *whitelistFilter) Name() string { return filterTypeWhitelist }

func (f *whitelistFilter) Check(tbCtx tb.Context, chatCfg *config.AgentConfig) bool {
	msg := tbCtx.Message()
	if msg == nil {
		return false
	}

	// Check each filter setting
	for _, filterCfg := range chatCfg.Filters.Filters {
		if filterCfg.Type != filterTypeWhitelist {
			continue
		}

		// If whitelist is configured, check membership
		for _, allowedID := range filterCfg.Whitelist {
			if msg.Chat.ID == allowedID || (msg.Sender != nil && msg.Sender.ID == allowedID) {
				return true
			}
		}

		// Whitelist configured but user not in it
		if msg.Sender != nil {
			zap.L().Debug("agentv3/filter: user not in whitelist",
				zap.Int64("user_id", msg.Sender.ID),
				zap.String("chat", chatCfg.Name),
			)
		}
		return false
	}

	// No whitelist filter configured = allow all
	return true
}

// ProcessFilters runs all configured filters on the message.
// Returns true if the message should be processed, false to block.
func ProcessFilters(tbCtx tb.Context, chatCfg *config.AgentConfig) bool {
	if config.BotConfig.WhiteListConfig.Enabled &&
		!config.BotConfig.WhiteListConfig.Check(tbCtx.Chat().ID) {
		zap.L().Debug("agentv3/filter: chat not in global whitelist",
			zap.Int64("chat_id", tbCtx.Chat().ID),
			zap.String("chat", chatCfg.Name),
		)
		return false
	}

	if len(chatCfg.Filters.Filters) == 0 {
		return true
	}

	filters := buildFilters(chatCfg)
	for _, f := range filters {
		if !f.Check(tbCtx, chatCfg) {
			zap.L().Debug("agentv3/filter: blocked by filter",
				zap.String("filter", f.Name()),
				zap.String("chat", chatCfg.Name),
			)
			return false
		}
	}

	return true
}

// buildFilters creates filter instances from config.
func buildFilters(chatCfg *config.AgentConfig) []Filter {
	var filters []Filter
	seen := make(map[string]bool)

	for _, filterCfg := range chatCfg.Filters.Filters {
		if seen[filterCfg.Type] {
			continue
		}
		seen[filterCfg.Type] = true

		switch filterCfg.Type {
		case filterTypeWhitelist:
			filters = append(filters, &whitelistFilter{})
		default:
			zap.L().Warn("agentv3/filter: unknown filter type",
				zap.String("type", filterCfg.Type),
			)
		}
	}

	return filters
}
