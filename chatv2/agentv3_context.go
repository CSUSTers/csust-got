//go:build !386 && !arm

package chatv2

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"csust-got/config"
	"csust-got/orm"

	"github.com/cloudwego/eino/schema"
	tb "gopkg.in/telebot.v3"
)

const agentV3Platform = "tg"

var (
	errAgentV3DynamicSystemPrompt    = errors.New("agent v3 system_prompt references dynamic field")
	errAgentV3ConfigNil              = errors.New("agent v3 config is nil")
	errAgentV3RuntimeDisabled        = errors.New("agent v3 runtime is disabled")
	errAgentV3RuntimeModeUnsupported = errors.New("agent v3 runtime mode is unsupported")
	errAgentV3SkillsModeUnsupported  = errors.New("agent v3 skills mode is unsupported")
	errAgentV3SkillsRootUnsupported  = errors.New("agent v3 skills root is unsupported")
)

// AgentV3TurnState stores per-turn agent-v3 context metadata.
type AgentV3TurnState struct {
	Scope                 orm.AgentV3Scope
	RunID                 string
	Namespace             string
	PrefixHash            string
	PrefixVersion         int64
	PromptCacheKey        string
	MemorySnapshotHash    string
	MemorySnapshotVersion int64
	SummaryVersion        int64
	RawTurnCount          int
	ToolDefsHash          string
	Trace                 *AgentV3Trace
}

func prepareAgentV3Turn(ctx context.Context, cc *CompiledChat, tc *TurnContext) ([]*schema.Message, error) {
	var cfg *config.AgentV3Config
	if config.BotConfig != nil {
		cfg = config.BotConfig.AgentV3
	}
	if cfg == nil {
		cfg = &config.AgentV3Config{}
	}
	if err := validateAgentV3RuntimeConfig(cfg); err != nil {
		return nil, err
	}

	tc.RunID = newAgentV3RunID()
	scope := orm.AgentV3Scope{
		Bot:      agentV3BotName(tc),
		Platform: agentV3Platform,
		ChatID:   tc.ChatID,
	}
	tc.Namespace = agentV3NamespaceFromScope(scope)
	tc.RuntimeClient = NewRemoteRuntimeClient(&cfg.Runtime, cfg.RuntimeCommandTimeout(), cfg.RuntimeRequestTimeout())

	trace := NewAgentV3Trace(tc.RunID, tc.ChatID, tc.Message.ID)
	tc.V3 = &AgentV3TurnState{
		Scope:     scope,
		RunID:     tc.RunID,
		Namespace: tc.Namespace,
		Trace:     trace,
	}
	finishContextSpan := trace.StartSpan("context_build", map[string]any{
		"agent": cc.Name,
	})
	soul, err := renderAgentV3Soul(cc, tc)
	if err != nil {
		finishContextSpan(err, nil)
		return nil, err
	}
	memoryText := ""
	var memoryVersion int64
	memoryHash := hashString("")
	finishMemorySpan := trace.StartSpan("memory_snapshot", map[string]any{
		"enabled": cfg.Memory.Enable,
	})
	if cfg.Memory.Enable {
		snapshot, err := orm.AgentV3GetMemorySnapshot(ctx, scope)
		if err != nil {
			err = fmt.Errorf("agent v3 memory snapshot: %w", err)
			finishMemorySpan(err, nil)
			finishContextSpan(err, nil)
			return nil, err
		}
		if snapshot != nil {
			memoryText = snapshot.Content
			memoryVersion = snapshot.Version
			memoryHash = snapshot.Hash
		}
	}
	finishMemorySpan(nil, map[string]any{
		"version": memoryVersion,
		"hash":    memoryHash,
		"chars":   len(memoryText),
	})

	toolDefs := agentV3ToolDefinitionsText()
	toolDefsHash := hashString(toolDefs)
	soulHash := hashString(soul)
	skillPromptBlock := buildAgentV3SkillPromptBlock(buildAgentV3BuiltinSkills(tc, cfg))
	skillPromptBlockHash := hashString(skillPromptBlock)
	prefixHash := buildAgentV3PrefixHash(soulHash, memoryHash, toolDefsHash, skillPromptBlockHash)
	modelName := agentV3ModelName(tc.Config)
	prefixVersion := int64(1)
	promptCacheKey := ""
	prefixText := buildAgentV3StablePrefix(soul, memoryText, toolDefs, skillPromptBlock)

	cacheHit := false
	finishCacheSpan := trace.StartSpan("context_cache", map[string]any{
		"enabled": cfg.ContextCache.Enable,
		"model":   modelName,
	})
	if cfg.ContextCache.Enable {
		current, err := orm.AgentV3GetPrefixCurrent(ctx, scope, cc.Name, modelName)
		if err != nil {
			err = fmt.Errorf("agent v3 prefix current: %w", err)
			finishCacheSpan(err, nil)
			finishContextSpan(err, nil)
			return nil, err
		}
		if current != nil {
			if current.Hash == prefixHash {
				prefixVersion = current.Version
				promptCacheKey = current.PromptCacheKey
				cacheHit = true
			} else {
				prefixVersion = current.Version + 1
			}
		}
		if promptCacheKey == "" {
			promptCacheKey = buildAgentV3PromptCacheKey(scope, cc.Name, modelName, prefixVersion)
		}
		rec := orm.AgentV3PrefixRecord{
			Agent:                 cc.Name,
			Model:                 modelName,
			Version:               prefixVersion,
			Hash:                  prefixHash,
			SoulHash:              soulHash,
			MemorySnapshotHash:    memoryHash,
			MemorySnapshotVersion: memoryVersion,
			ToolDefsHash:          toolDefsHash,
			PromptCacheKey:        promptCacheKey,
			UpdatedAt:             time.Now(),
		}
		if err := orm.AgentV3SetPrefix(ctx, scope, rec, prefixText, cfg.ContextCacheTTL()); err != nil {
			err = fmt.Errorf("agent v3 prefix set: %w", err)
			finishCacheSpan(err, nil)
			finishContextSpan(err, nil)
			return nil, err
		}
	} else {
		promptCacheKey = buildAgentV3PromptCacheKey(scope, cc.Name, modelName, prefixVersion)
	}
	finishCacheSpan(nil, map[string]any{
		"cache_hit":             cacheHit,
		"prefix_hash":           prefixHash,
		"prefix_version":        prefixVersion,
		"prompt_cache_key_hash": hashString(promptCacheKey),
	})

	finishHotAppendSpan := trace.StartSpan("hot_append", nil)
	summary, summaryVersion, err := orm.AgentV3GetSummary(ctx, scope)
	if err != nil {
		err = fmt.Errorf("agent v3 summary: %w", err)
		finishHotAppendSpan(err, nil)
		finishContextSpan(err, nil)
		return nil, err
	}
	rawTurns, err := orm.AgentV3LoadTurns(ctx, scope, cfg.ContextCache.RawTurns)
	if err != nil {
		err = fmt.Errorf("agent v3 raw turns: %w", err)
		finishHotAppendSpan(err, nil)
		finishContextSpan(err, nil)
		return nil, err
	}
	rawTurns = trimAgentV3TurnsByMaxChars(rawTurns, approxAgentV3TokenCharLimit(cfg.ContextCache.MaxRawTokens))
	finishHotAppendSpan(nil, map[string]any{
		"summary_version": summaryVersion,
		"summary_chars":   len(summary),
		"raw_turn_count":  len(rawTurns),
	})

	trace.PrefixHash = prefixHash
	trace.PrefixVersion = prefixVersion
	trace.PromptCacheKeyHash = hashString(promptCacheKey)
	trace.MemorySnapshotVersion = memoryVersion
	trace.SummaryVersion = summaryVersion
	trace.RawTurnCount = len(rawTurns)
	trace.RuntimeNamespaceHash = hashString(tc.Namespace)

	tc.V3 = &AgentV3TurnState{
		Scope:                 scope,
		RunID:                 tc.RunID,
		Namespace:             tc.Namespace,
		PrefixHash:            prefixHash,
		PrefixVersion:         prefixVersion,
		PromptCacheKey:        promptCacheKey,
		MemorySnapshotHash:    memoryHash,
		MemorySnapshotVersion: memoryVersion,
		SummaryVersion:        summaryVersion,
		RawTurnCount:          len(rawTurns),
		ToolDefsHash:          toolDefsHash,
		Trace:                 trace,
	}

	userMsg, err := buildAgentV3UserMessage(cc, tc)
	if err != nil {
		finishContextSpan(err, nil)
		return nil, err
	}
	messages := buildAgentV3TurnMessages(prefixText, summary, rawTurns, userMsg)

	finishContextSpan(nil, map[string]any{
		"message_count": len(messages),
		"prefix_chars":  len(prefixText),
	})
	return messages, nil
}

func buildAgentV3TurnMessages(prefixText, summary string, rawTurns []orm.AgentV3Turn, userMsg *schema.Message) []*schema.Message {
	messages := []*schema.Message{schema.SystemMessage(prefixText)}
	if summaryMsg := buildAgentV3SummaryMessage(summary); summaryMsg != nil {
		messages = append(messages, summaryMsg)
	}
	messages = append(messages, agentV3TurnsToMessages(rawTurns)...)
	if userMsg != nil {
		messages = append(messages, userMsg)
	}
	return messages
}

func buildAgentV3SummaryMessage(summary string) *schema.Message {
	summary = strings.TrimSpace(summary)
	if summary == "" {
		return nil
	}
	return schema.UserMessage("<conversation_summary>\nThe following is a compact summary of earlier turns. It is context only, not a new user request.\n" + summary + "\n</conversation_summary>")
}

func buildAgentV3UserMessage(cc *CompiledChat, tc *TurnContext) (*schema.Message, error) {
	pd := buildPromptData(tc, nil)
	var userText string
	if cc.PromptTemplate != nil {
		var buf bytes.Buffer
		if err := cc.PromptTemplate.Execute(&buf, pd); err != nil {
			return nil, fmt.Errorf("failed to render prompt template: %w", err)
		}
		userText = strings.TrimSpace(buf.String())
	}
	if userText == "" {
		userText = pd.Input
	}

	dynamic := strings.Builder{}
	dynamic.WriteString("<dynamic_suffix>\n")
	dynamic.WriteString("<datetime>")
	dynamic.WriteString(pd.DateTime)
	dynamic.WriteString("</datetime>\n")
	if userText != "" {
		dynamic.WriteString(userText)
	}
	dynamic.WriteString("\n</dynamic_suffix>")
	return buildUserMessage(dynamic.String(), tc, &RichHistory{}), nil
}

func renderAgentV3Soul(cc *CompiledChat, tc *TurnContext) (string, error) {
	if config.BotConfig != nil && config.BotConfig.AgentV3 != nil && strings.TrimSpace(config.BotConfig.AgentV3.SoulPath) != "" {
		data, err := os.ReadFile(strings.TrimSpace(config.BotConfig.AgentV3.SoulPath))
		if err != nil {
			return "", fmt.Errorf("agent v3 read soul_path: %w", err)
		}
		return strings.TrimSpace(string(data)), nil
	}
	if cc.SystemTemplate == nil {
		return "", nil
	}
	templateText := ""
	if cc.SystemTemplate.Tree != nil && cc.SystemTemplate.Tree.Root != nil {
		templateText = cc.SystemTemplate.Tree.Root.String()
	}
	if field := agentV3DynamicSystemField(templateText); field != "" {
		return "", fmt.Errorf("%w %s; move it to prompt_template or dynamic suffix", errAgentV3DynamicSystemPrompt, field)
	}
	pd := PromptData{}
	if tc.BotUser != nil {
		pd.BotUsername = tc.BotUser.Username
	}
	var buf bytes.Buffer
	if err := cc.SystemTemplate.Execute(&buf, pd); err != nil {
		return "", fmt.Errorf("failed to render system prompt: %w", err)
	}
	return strings.TrimSpace(buf.String()), nil
}

func buildAgentV3PrefixHash(soulHash, memoryHash, toolDefsHash, skillPromptBlockHash string) string {
	return hashString(strings.Join([]string{soulHash, memoryHash, toolDefsHash, skillPromptBlockHash}, ":"))
}

func buildAgentV3StablePrefix(soul, memory, toolDefs, skillPromptBlock string) string {
	var parts []string
	if strings.TrimSpace(soul) != "" {
		parts = append(parts, "<soul>\n"+strings.TrimSpace(soul)+"\n</soul>")
	}
	if strings.TrimSpace(memory) != "" {
		parts = append(parts, "<group_memory_snapshot>\n"+strings.TrimSpace(memory)+"\n</group_memory_snapshot>")
	} else {
		parts = append(parts, "<group_memory_snapshot>\n(empty)\n</group_memory_snapshot>")
	}
	parts = append(parts, "<runtime_and_skill_rules>\n"+agentV3RuntimeSkillRules()+"\n</runtime_and_skill_rules>")
	if strings.TrimSpace(skillPromptBlock) != "" {
		parts = append(parts, strings.TrimSpace(skillPromptBlock))
	}
	parts = append(parts, "<tool_definitions>\n"+toolDefs+"\n</tool_definitions>")
	return strings.Join(parts, "\n\n")
}

func agentV3RichMessageSkillContract(enabled bool) string {
	if !enabled {
		return ""
	}
	return strings.Join([]string{
		"Telegram Rich Message output is available for this chat.",
		"Use normal plain text when rich layout is unnecessary.",
		"When rich output is useful, make your final answer exactly one <telegram_rich_message> envelope and no surrounding prose.",
		"The envelope body must be raw Telegram Rich Markdown, not JSON, not HTML, and not an InputRichMessage object.",
		"Do not emit mode fields, fallback fields, explicit block AST payloads, media uploads, or sendRichMessageDraft instructions.",
		"Rich Markdown may use supported structural syntax such as headings, lists, task lists, quotes, code blocks, tables, and details.",
		"The bot derives plain fallback text from your Rich Markdown, so keep the Markdown semantically complete without relying on hidden metadata.",
		"Example: <telegram_rich_message># Title\n\n**Body**</telegram_rich_message>",
	}, "\n")
}

func agentV3RuntimeSkillRules() string {
	return "You are running in agent-v3 mode.\n" +
		"Visible tools are fixed to read, grep, write, edit, bash.\n" +
		"Use the remote runtime namespace for this chat only; never assume access to another chat workspace.\n" +
		"Available skills, if any, are already injected into <agent_v3_skills> in this system prompt.\n" +
		"Do not use read, grep, or runtime filesystem paths to load skills from /skills.\n" +
		"If an injected skill documents bash commands, run only those explicitly documented commands and arguments.\n" +
		"Do not invent skill commands or /skills scripts.\n" +
		"Do not write skill instructions into long-term memory.\n" +
		"Use bash for command execution only through the remote runtime."
}

func agentV3TurnsToMessages(turns []orm.AgentV3Turn) []*schema.Message {
	out := make([]*schema.Message, 0, len(turns))
	for _, turn := range turns {
		content := strings.TrimSpace(turn.Content)
		if content == "" {
			continue
		}
		switch turn.Role {
		case string(schema.Assistant):
			out = append(out, schema.AssistantMessage(content, nil))
		default:
			out = append(out, schema.UserMessage(content))
		}
	}
	return out
}

func saveAgentV3TurnPair(ctx context.Context, tc *TurnContext, userInput, assistantOutput string, assistantMsgID int) error {
	if tc == nil || tc.V3 == nil || config.BotConfig == nil || config.BotConfig.AgentV3 == nil {
		return nil
	}
	var finishSpan func(error, map[string]any)
	if tc.V3.Trace != nil {
		attrs := map[string]any{
			"assistant_message_id": assistantMsgID,
			"assistant_chars":      len(assistantOutput),
			"user_chars":           len(userInput),
		}
		if preview, ok := agentV3TracePreview(assistantOutput); ok {
			attrs["output_preview"] = preview
		}
		finishSpan = tc.V3.Trace.StartSpan("final_output", attrs)
	}
	ttl := config.BotConfig.AgentV3.ContextCacheTTL()
	maxTurns := config.BotConfig.AgentV3.ContextCache.RawTurns
	if strings.TrimSpace(userInput) != "" {
		messageID := 0
		if tc.Message != nil {
			messageID = tc.Message.ID
		}
		if err := orm.AgentV3AppendTurn(ctx, tc.V3.Scope, orm.AgentV3Turn{
			Role:      string(schema.User),
			Content:   userInput,
			MessageID: messageID,
			CreatedAt: time.Now(),
		}, maxTurns, ttl); err != nil {
			err = fmt.Errorf("agent v3 append user turn: %w", err)
			if finishSpan != nil {
				finishSpan(err, nil)
			}
			return err
		}
	}
	if strings.TrimSpace(assistantOutput) != "" {
		if err := orm.AgentV3AppendTurn(ctx, tc.V3.Scope, orm.AgentV3Turn{
			Role:      string(schema.Assistant),
			Content:   assistantOutput,
			MessageID: assistantMsgID,
			CreatedAt: time.Now(),
		}, maxTurns, ttl); err != nil {
			err = fmt.Errorf("agent v3 append assistant turn: %w", err)
			if finishSpan != nil {
				finishSpan(err, nil)
			}
			return err
		}
	}
	err := rebuildAgentV3Summary(ctx, tc)
	if finishSpan != nil {
		finishSpan(err, map[string]any{"turn_saved": err == nil})
	}
	return err
}

func maybeRememberExplicitInput(ctx context.Context, tc *TurnContext, input string) error {
	if tc == nil || tc.V3 == nil || config.BotConfig == nil || config.BotConfig.AgentV3 == nil || !config.BotConfig.AgentV3.Memory.Enable {
		return nil
	}
	content := extractExplicitMemoryContent(input)
	if content == "" {
		return nil
	}
	var senderID int64
	if tc.Message != nil && tc.Message.Sender != nil {
		senderID = tc.Message.Sender.ID
	}
	return addAgentV3Memory(ctx, tc.V3.Scope, senderID, content)
}

func extractExplicitMemoryContent(input string) string {
	input = strings.TrimSpace(input)
	for _, prefix := range []string{"记住：", "记住:", "记住 ", "请记住：", "请记住:"} {
		if strings.HasPrefix(input, prefix) {
			return strings.TrimSpace(strings.TrimPrefix(input, prefix))
		}
	}
	if input == "记住" {
		return ""
	}
	return ""
}

func addAgentV3Memory(ctx context.Context, scope orm.AgentV3Scope, createdBy int64, content string) error {
	content = strings.TrimSpace(content)
	if content == "" {
		return nil
	}
	cfg := config.BotConfig.AgentV3
	ttl := cfg.ContextCacheTTL()
	item := orm.AgentV3MemoryItem{
		ID:        newAgentV3MemoryID(),
		Content:   content,
		CreatedBy: createdBy,
		CreatedAt: time.Now(),
	}
	if err := orm.AgentV3AddMemory(ctx, scope, item, ttl); err != nil {
		return err
	}
	return rebuildAgentV3MemorySnapshot(ctx, scope, ttl)
}

func rebuildAgentV3MemorySnapshot(ctx context.Context, scope orm.AgentV3Scope, ttl time.Duration) error {
	items, err := orm.AgentV3ListMemory(ctx, scope)
	if err != nil {
		return err
	}
	sort.Slice(items, func(i, j int) bool {
		return items[i].CreatedAt.Before(items[j].CreatedAt)
	})
	lines := make([]string, 0, len(items))
	for _, item := range items {
		if strings.TrimSpace(item.Content) == "" {
			continue
		}
		lines = append(lines, "- "+strings.TrimSpace(item.Content))
	}
	content := strings.Join(lines, "\n")
	if config.BotConfig != nil && config.BotConfig.AgentV3 != nil {
		content = truncateAgentV3Text(content, approxAgentV3TokenCharLimit(config.BotConfig.AgentV3.Memory.SnapshotMaxTokens))
	}
	current, err := orm.AgentV3GetMemorySnapshot(ctx, scope)
	if err != nil {
		return err
	}
	version := int64(1)
	if current != nil {
		version = current.Version + 1
	}
	return orm.AgentV3SetMemorySnapshot(ctx, scope, orm.AgentV3MemorySnapshot{
		Version:   version,
		Hash:      hashString(content),
		Content:   content,
		UpdatedAt: time.Now(),
	}, ttl)
}

func agentV3ScopeFromContext(ctx tb.Context) orm.AgentV3Scope {
	bot := agentV3DefaultBotName
	if ctx != nil && ctx.Bot() != nil && ctx.Bot().Me != nil && ctx.Bot().Me.Username != "" {
		bot = ctx.Bot().Me.Username
	}
	chatID := int64(0)
	if ctx != nil && ctx.Chat() != nil {
		chatID = ctx.Chat().ID
	}
	return orm.AgentV3Scope{Bot: bot, Platform: agentV3Platform, ChatID: chatID}
}

func agentV3BotName(tc *TurnContext) string {
	if tc != nil && tc.BotUser != nil && tc.BotUser.Username != "" {
		return tc.BotUser.Username
	}
	return agentV3DefaultBotName
}

func agentV3NamespaceFromScope(scope orm.AgentV3Scope) string {
	bot := scope.Bot
	if bot == "" {
		bot = agentV3DefaultBotName
	}
	platform := scope.Platform
	if platform == "" {
		platform = agentV3Platform
	}
	return fmt.Sprintf("%s:%s:%d", bot, platform, scope.ChatID)
}

func buildAgentV3PromptCacheKey(scope orm.AgentV3Scope, agent, model string, version int64) string {
	return fmt.Sprintf("csust:%s:%d:%s:%s:v%d", scope.Bot, scope.ChatID, agent, model, version)
}

func rebuildAgentV3Summary(ctx context.Context, tc *TurnContext) error {
	if tc == nil || tc.V3 == nil || config.BotConfig == nil || config.BotConfig.AgentV3 == nil {
		return nil
	}
	cfg := config.BotConfig.AgentV3
	limit := cfg.ContextCache.SummaryTurns + cfg.ContextCache.RawTurns
	turns, err := orm.AgentV3LoadRecentTurns(ctx, tc.V3.Scope, limit)
	if err != nil {
		return fmt.Errorf("agent v3 load summary turns: %w", err)
	}
	if len(turns) <= cfg.ContextCache.RawTurns {
		return nil
	}
	summaryTurns := turns[:len(turns)-cfg.ContextCache.RawTurns]
	content := summarizeAgentV3Turns(summaryTurns, cfg.ContextCache.MaxSummaryTokens)
	current, version, err := orm.AgentV3GetSummary(ctx, tc.V3.Scope)
	if err != nil {
		return fmt.Errorf("agent v3 get current summary: %w", err)
	}
	if strings.TrimSpace(current) == strings.TrimSpace(content) {
		return nil
	}
	if version <= 0 {
		version = 1
	} else {
		version++
	}
	return orm.AgentV3SetSummary(ctx, tc.V3.Scope, orm.AgentV3Summary{
		Version:   version,
		Hash:      hashString(content),
		Content:   content,
		UpdatedAt: time.Now(),
	}, cfg.ContextCacheTTL())
}

func summarizeAgentV3Turns(turns []orm.AgentV3Turn, maxTokens int) string {
	if len(turns) == 0 {
		return ""
	}
	lines := make([]string, 0, len(turns))
	for _, turn := range turns {
		content := compactAgentV3Text(turn.Content)
		if content == "" {
			continue
		}
		role := turn.Role
		if role == "" {
			role = "user"
		}
		lines = append(lines, fmt.Sprintf("- %s: %s", role, truncateAgentV3Text(content, 600)))
	}
	return truncateAgentV3Text(strings.Join(lines, "\n"), approxAgentV3TokenCharLimit(maxTokens))
}

func trimAgentV3TurnsByMaxChars(turns []orm.AgentV3Turn, maxChars int) []orm.AgentV3Turn {
	if maxChars <= 0 || len(turns) == 0 {
		return turns
	}
	total := 0
	start := len(turns) - 1
	for i := len(turns) - 1; i >= 0; i-- {
		nextTotal := total + len(turns[i].Content)
		if nextTotal > maxChars && i != len(turns)-1 {
			break
		}
		total = nextTotal
		start = i
	}
	return turns[start:]
}

func compactAgentV3Text(s string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(s)), " ")
}

func approxAgentV3TokenCharLimit(tokens int) int {
	if tokens <= 0 {
		return 0
	}
	return tokens * 4
}

func truncateAgentV3Text(s string, limit int) string {
	if limit <= 0 || len(s) <= limit {
		return s
	}
	end := limit
	for end > 0 && !utf8.ValidString(s[:end]) {
		end--
	}
	if end <= 0 {
		return ""
	}
	return s[:end] + "\n[truncated]"
}

func agentV3DynamicSystemField(templateText string) string {
	for _, field := range []string{".DateTime", ".CurrentDateCN", ".Input", ".ContextMessages", ".ContextText", ".ContextXml", ".ReplyToXml"} {
		if strings.Contains(templateText, field) {
			return field
		}
	}
	return ""
}

func agentV3ModelName(chatCfg *config.ChatConfigSingle) string {
	if config.BotConfig != nil && config.BotConfig.AgentV3 != nil {
		if m := config.BotConfig.AgentV3.EffectiveModel(chatCfg.Model); m != nil {
			return m.Model
		}
	}
	if chatCfg != nil && chatCfg.Model != nil {
		return chatCfg.Model.Model
	}
	return ""
}

func newAgentV3RunID() string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("run_%d", time.Now().UnixNano())
	}
	return "run_" + hex.EncodeToString(b[:])
}

func newAgentV3MemoryID() string {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("mem_%d", time.Now().UnixNano())
	}
	return "mem_" + hex.EncodeToString(b[:])
}

func hashString(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func validateAgentV3RuntimeConfig(cfg *config.AgentV3Config) error {
	if cfg == nil {
		return errAgentV3ConfigNil
	}
	if !cfg.Runtime.Enable {
		return errAgentV3RuntimeDisabled
	}
	if cfg.Runtime.Mode != "" && cfg.Runtime.Mode != agentV3RuntimeModeRemoteHTTP {
		return fmt.Errorf("%w: %q; expected %s", errAgentV3RuntimeModeUnsupported, cfg.Runtime.Mode, agentV3RuntimeModeRemoteHTTP)
	}
	if cfg.Skills.Mode != "" && cfg.Skills.Mode != agentV3SkillsModeSystemPrompt {
		return fmt.Errorf("%w: %q; expected %s", errAgentV3SkillsModeUnsupported, cfg.Skills.Mode, agentV3SkillsModeSystemPrompt)
	}
	if cfg.Skills.Root != "" {
		return fmt.Errorf("%w: %q; expected empty", errAgentV3SkillsRootUnsupported, cfg.Skills.Root)
	}
	return nil
}
