//go:build !386 && !arm

package chatv2

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"csust-got/config"
	"csust-got/orm"
	"csust-got/util"

	tb "gopkg.in/telebot.v3"
)

func MemoryCommand(ctx tb.Context) error {
	scope := agentV3ScopeFromContext(ctx)
	payload := agentV3CommandPayload(ctx)
	if payload == "" {
		return ctx.Reply("用法：/memory add 内容、/memory list、/memory forget <id>")
	}
	cmd, rest, _ := strings.Cut(payload, " ")
	cmd = strings.ToLower(strings.TrimSpace(cmd))
	rest = strings.TrimSpace(rest)

	switch cmd {
	case "add":
		if !canManageAgentV3Memory(ctx) {
			return ctx.Reply("只有管理员可以写入群记忆。")
		}
		if rest == "" {
			return ctx.Reply("要记住的内容不能为空。")
		}
		if err := addAgentV3Memory(context.Background(), scope, agentV3SenderID(ctx), rest); err != nil {
			return ctx.Reply("写入 memory 失败：" + err.Error())
		}
		return ctx.Reply("已记住。")
	case "list":
		if !canManageAgentV3Memory(ctx) {
			return ctx.Reply("只有管理员可以查看群记忆。")
		}
		items, err := orm.AgentV3ListMemory(context.Background(), scope)
		if err != nil {
			return ctx.Reply("读取 memory 失败：" + err.Error())
		}
		if len(items) == 0 {
			return ctx.Reply("当前没有 memory。")
		}
		var b strings.Builder
		for _, item := range items {
			b.WriteString(item.ID)
			b.WriteString(": ")
			b.WriteString(item.Content)
			b.WriteByte('\n')
		}
		return replyAgentV3Pre(ctx, b.String())
	case "forget":
		if !canManageAgentV3Memory(ctx) {
			return ctx.Reply("只有管理员可以删除群记忆。")
		}
		if rest == "" {
			return ctx.Reply("请提供 memory id。")
		}
		if err := orm.AgentV3ForgetMemory(context.Background(), scope, rest); err != nil {
			return ctx.Reply("删除 memory 失败：" + err.Error())
		}
		if config.BotConfig != nil && config.BotConfig.AgentV3 != nil {
			if err := rebuildAgentV3MemorySnapshot(context.Background(), scope, config.BotConfig.AgentV3.ContextCacheTTL()); err != nil {
				return ctx.Reply("重建 memory snapshot 失败：" + err.Error())
			}
		}
		return ctx.Reply("已删除。")
	default:
		return ctx.Reply("未知 memory 子命令。")
	}
}

func TraceLastCommand(ctx tb.Context) error {
	if !canManageAgentV3Memory(ctx) {
		return ctx.Reply("只有管理员可以查看 agent-v3 trace。")
	}
	summary, err := orm.AgentV3GetTraceSummary(context.Background(), agentV3ScopeFromContext(ctx))
	if err != nil {
		return ctx.Reply("读取 trace 失败：" + err.Error())
	}
	if summary == nil {
		return ctx.Reply("还没有 agent v3 trace。")
	}
	data, _ := json.MarshalIndent(summary, "", "  ")
	return replyAgentV3Pre(ctx, string(data))
}

func ContextCacheCommand(ctx tb.Context) error {
	if !canManageAgentV3Memory(ctx) {
		return ctx.Reply("只有管理员可以查看 context cache。")
	}
	summary, err := orm.AgentV3GetTraceSummary(context.Background(), agentV3ScopeFromContext(ctx))
	if err != nil {
		return ctx.Reply("读取 context cache 失败：" + err.Error())
	}
	if summary == nil {
		return ctx.Reply("还没有 context cache 记录。")
	}
	cacheHit := "unknown"
	if v, ok := agentV3TraceSpanAttr(summary, "context_cache", "cache_hit"); ok {
		cacheHit = fmt.Sprint(v)
	}
	msg := fmt.Sprintf(
		"prefix_version: %d\nprefix_hash: %s\nprompt_cache_key_hash: %s\ncontext_cache_hit: %s\nmemory_snapshot_version: %d\nsummary_version: %d\nraw_turn_count: %d\nprompt_tokens: %d\ncached_tokens: %d",
		summary.PrefixVersion,
		shortHash(summary.PrefixHash),
		shortHash(summary.PromptCacheKeyHash),
		cacheHit,
		summary.MemorySnapshotVersion,
		summary.SummaryVersion,
		summary.RawTurnCount,
		summary.PromptTokens,
		summary.CachedTokens,
	)
	return replyAgentV3Pre(ctx, msg)
}

func agentV3TraceSpanAttr(summary *orm.AgentV3TraceSummary, spanName, attrName string) (any, bool) {
	if summary == nil {
		return nil, false
	}
	for _, span := range summary.Spans {
		if span.Name != spanName || span.Attrs == nil {
			continue
		}
		v, ok := span.Attrs[attrName]
		return v, ok
	}
	return nil, false
}

func RuntimeStatusCommand(ctx tb.Context) error {
	if !canManageAgentV3Memory(ctx) {
		return ctx.Reply("只有管理员可以查看 runtime 状态。")
	}
	if config.BotConfig == nil || config.BotConfig.AgentV3 == nil {
		return ctx.Reply("agent_v3 未配置。")
	}
	client := NewRemoteRuntimeClient(
		&config.BotConfig.AgentV3.Runtime,
		config.BotConfig.AgentV3.RuntimeCommandTimeout(),
		config.BotConfig.AgentV3.RuntimeRequestTimeout(),
	)
	status, err := client.Status(context.Background())
	if err != nil {
		return ctx.Reply("runtime 不可用：" + err.Error())
	}
	data, _ := json.MarshalIndent(status, "", "  ")
	return replyAgentV3Pre(ctx, string(data))
}

func RuntimeResetCommand(ctx tb.Context) error {
	if config.BotConfig == nil || config.BotConfig.AgentV3 == nil {
		return ctx.Reply("agent_v3 未配置。")
	}
	if !canManageAgentV3Memory(ctx) {
		return ctx.Reply("只有管理员可以重置本群 runtime workspace。")
	}
	payload := agentV3CommandPayload(ctx)
	if payload != "confirm" && payload != "确认" {
		return ctx.Reply("这会删除本聊天的 agent-v3 runtime workspace。确认请发送：/runtime_reset confirm")
	}
	client := NewRemoteRuntimeClient(
		&config.BotConfig.AgentV3.Runtime,
		config.BotConfig.AgentV3.RuntimeCommandTimeout(),
		config.BotConfig.AgentV3.RuntimeRequestTimeout(),
	)
	scope := agentV3ScopeFromContext(ctx)
	resp, err := client.Reset(context.Background(), runtimeResetRequest{
		runtimeCommonRequest: runtimeCommonRequest{
			Namespace: agentV3NamespaceFromScope(scope),
			RunID:     newAgentV3RunID(),
			Cwd:       "/workspace",
		},
	})
	if err != nil {
		return ctx.Reply("runtime reset 失败：" + err.Error())
	}
	data, _ := json.MarshalIndent(resp, "", "  ")
	return replyAgentV3Pre(ctx, string(data))
}

func canManageAgentV3Memory(ctx tb.Context) bool {
	if ctx.Chat() == nil || ctx.Chat().Type == tb.ChatPrivate {
		return true
	}
	if ctx.Sender() == nil {
		return false
	}
	return util.CanRestrictMembers(ctx.Chat(), ctx.Sender())
}

func agentV3CommandPayload(ctx tb.Context) string {
	if ctx == nil || ctx.Message() == nil {
		return ""
	}
	return strings.TrimSpace(ctx.Message().Payload)
}

func agentV3SenderID(ctx tb.Context) int64 {
	if ctx == nil || ctx.Sender() == nil {
		return 0
	}
	return ctx.Sender().ID
}

func replyAgentV3Pre(ctx tb.Context, text string) error {
	text = truncateAgentV3Text(text, 3500)
	_, err := util.ReplyWithError(ctx, util.RawTgText("<pre>"+util.EscapeTgHTMLReservedChars(text)+"</pre>"), tb.ModeHTML)
	return err
}

func shortHash(s string) string {
	if len(s) <= 12 {
		return s
	}
	return s[:12]
}
