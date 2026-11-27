package agentv3

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"csust-got/agent/session"
	"csust-got/config"
	"csust-got/orm"
	"csust-got/util"

	"github.com/cloudwego/eino/schema"
	"github.com/samber/lo"
	"go.uber.org/zap"
	tb "gopkg.in/telebot.v3"
)

const agentV3Platform = "tg"

var (
	errAgentV3DynamicSystemPrompt    = errors.New("agent v3 system_prompt references dynamic field")
	errAgentV3ConfigNil              = errors.New("agent v3 config is nil")
	errAgentV3RuntimeDisabled        = errors.New("agent v3 runtime is disabled")
	errAgentV3RuntimeModeUnsupported = errors.New("agent v3 runtime mode is unsupported")
	errAgentV3SkillsModeUnsupported  = errors.New("agent v3 skills mode is unsupported")
)

// AgentV3TurnState stores per-turn agent-v3 context metadata.
type AgentV3TurnState struct {
	Scope          orm.AgentV3Scope
	RunID          string
	Namespace      string
	PrefixHash     string
	PrefixVersion  int64
	PromptCacheKey string
	SummaryVersion int64
	RawTurnCount   int
	ToolDefsHash   string
	ImageRefs      []orm.AgentV3ImageRef
	Trace          *AgentV3Trace
	SkillCatalog   agentV3SkillCatalog
	runtimeEnv     map[string]string
	renderCtx      context.Context
	frameTime      time.Time
}

func prepareAgentV3Turn(ctx context.Context, cc *CompiledAgent, tc *TurnContext, history *RichHistory) ([]*schema.Message, error) {
	if history == nil {
		history = &RichHistory{}
	}
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
	catalog, _, err := mergeAgentV3SkillSnapshots(cc.AgentV3SkillSources...)
	if err != nil {
		return nil, fmt.Errorf("agent v3 turn skill catalog: %w", err)
	}
	if !tc.Background && tc.Session != nil && tc.Session.runID != "" {
		tc.RunID = tc.Session.runID
	} else if !tc.Background || tc.RunID == "" {
		tc.RunID = newAgentV3RunID()
	}
	scope := orm.AgentV3Scope{
		Bot:      agentV3BotName(tc),
		Platform: agentV3Platform,
		ChatID:   tc.ChatID,
	}
	tc.Namespace = agentV3NamespaceFromScope(scope)
	tc.RuntimeClient = NewRemoteRuntimeClient(&cfg.Runtime, cfg.RuntimeCommandTimeout(), cfg.RuntimeRequestTimeout())

	trace := NewAgentV3Trace(tc.RunID, tc.ChatID, tc.Message.ID)
	trace.SetContentRedacted(tc.Background)
	tc.V3 = &AgentV3TurnState{
		Scope:        scope,
		RunID:        tc.RunID,
		Namespace:    tc.Namespace,
		Trace:        trace,
		SkillCatalog: catalog,
		runtimeEnv:   cloneAgentV3SkillEnvironment(cfg.Runtime.Env),
		renderCtx:    ctx,
		frameTime:    beijingNow(),
	}
	finishContextSpan := trace.StartSpan("context_build", map[string]any{
		"agent": cc.Name,
	})
	soul, err := renderAgentV3Soul(cc, tc)
	if err != nil {
		finishContextSpan(err, nil)
		return nil, err
	}
	var directory string
	finishMemorySpan := trace.StartSpan("memory_directory", map[string]any{"enabled": cfg.Memory.Enable})
	if cfg.Memory.Enable {
		directories, err := orm.GroupMemoryCatalog(ctx, scope)
		if err != nil {
			err = fmt.Errorf("agent v3 memory directory: %w", err)
			finishMemorySpan(err, nil)
			finishContextSpan(err, nil)
			return nil, err
		}
		lines := make([]string, 0, len(directories))
		for _, dir := range directories {
			lines = append(lines, fmt.Sprintf("- %s > %s：%s", dir.Category, dir.Directory, dir.Description))
		}
		directory = strings.Join(lines, "\n")
	}
	finishMemorySpan(nil, map[string]any{"chars": utf8.RuneCountInString(directory)})

	includeLoadSkill := len(catalog.Sorted) > 0
	fetchEnabled := cfg.RuntimeFetchEnabled()
	searxngEnabled := cc.AgentV3StartupSkills != nil && cc.AgentV3StartupSkills.SearXNG != nil
	toolDefs := agentV3ToolDefinitionsText(includeLoadSkill, fetchEnabled, searxngEnabled, cfg.CronConfig().RunnerAgent != "", cfg.Memory.Enable)
	toolDefsHash := hashString(toolDefs)
	soulHash := hashString(soul)
	skillPromptBlock := buildAgentV3SkillPromptBlock(catalog.Sorted)
	prefixText := buildAgentV3StablePrefix(soul, skillPromptBlock, fetchEnabled)
	prefixHash := hashString(prefixText)
	modelName := agentV3ModelName(tc.Config)
	prefixVersion := int64(1)
	promptCacheKey := ""

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
			promptCacheKey = buildAgentV3PromptCacheKey(scope, modelName, prefixVersion)
		}
		rec := orm.AgentV3PrefixRecord{
			Agent:          cc.Name,
			Model:          modelName,
			Version:        prefixVersion,
			Hash:           prefixHash,
			SoulHash:       soulHash,
			ToolDefsHash:   toolDefsHash,
			PromptCacheKey: promptCacheKey,
			UpdatedAt:      time.Now(),
		}
		if err := orm.AgentV3SetPrefix(ctx, scope, rec, cfg.ContextCacheTTL()); err != nil {
			err = fmt.Errorf("agent v3 prefix set: %w", err)
			finishCacheSpan(err, nil)
			finishContextSpan(err, nil)
			return nil, err
		}
	} else {
		promptCacheKey = buildAgentV3PromptCacheKey(scope, modelName, prefixVersion)
	}
	finishCacheSpan(nil, map[string]any{
		"cache_hit":             cacheHit,
		"prefix_hash":           prefixHash,
		"prefix_version":        prefixVersion,
		"prompt_cache_key_hash": hashString(promptCacheKey),
	})

	trace.PrefixHash = prefixHash
	trace.PrefixVersion = prefixVersion
	trace.PromptCacheKeyHash = hashString(promptCacheKey)
	trace.RuntimeNamespaceHash = hashString(tc.Namespace)
	tc.V3.PrefixHash, tc.V3.PrefixVersion, tc.V3.PromptCacheKey = prefixHash, prefixVersion, promptCacheKey
	tc.V3.ToolDefsHash = toolDefsHash

	frame := []*schema.Message{schema.SystemMessage(prefixText)}
	if directory != "" {
		frame = append(frame, schema.UserMessage("<group_memory_directory>\n群记忆目录（仅供参考）：\n"+directory+"\n</group_memory_directory>"))
	}
	var accepted agentV3PreparedSessionInput
	rebuilt := false
	state := tc.Session
	if !tc.Background && state != nil && state.load {
		state.load = false
		var prepared agentV3PreparedSessionInput
		loaded, loadErr := state.service.LoadWithAcceptance(ctx, state.selection, func(op context.Context, candidate *session.LoadCandidate) error {
			candidateV3 := *tc.V3
			candidateV3.renderCtx = op
			candidateTC := &TurnContext{Bot: tc.Bot, BotUser: tc.BotUser, Message: tc.Message, ChatID: tc.ChatID, Config: tc.Config, Trigger: tc.Trigger, V3: &candidateV3}
			var err error
			prepared, err = buildAgentV3LoadedInput(cc, candidateTC, frame, candidate.Messages, candidate)
			if err != nil {
				return err
			}
			if err := op.Err(); err != nil {
				return err
			}
			limit := tc.Config.EffectiveSessionTokenLimit(cfg.Session)
			estimate, err := cc.Agent.estimateSessionContext(op, prepared.messages, limit)
			if err != nil {
				return fmt.Errorf("session context estimate unknown: %w", err)
			}
			if estimate.Tokens > limit || estimate.Capped {
				rebuilt = true
				zap.L().Debug("agentv3: overflow_rebuild", zap.String("agent", tc.Config.Name), zap.Int64("chat_id", tc.ChatID), zap.String("strategy", cfg.Session.ContextOverflow.StrategyName()), zap.Int64("max_tokens", limit), zap.Int64("estimated_tokens", estimate.Tokens), zap.Bool("estimate_capped", estimate.Capped), zap.String("estimate_method", estimate.Method))
				return errAgentV3SessionContextOverflow
			}
			return op.Err()
		})
		if loadErr == nil {
			state.parent, state.replay = loaded.Parent, loaded.Messages
			tc.V3.ImageRefs = prepared.imageRefs
			accepted = prepared
		} else {
			prepared = agentV3PreparedSessionInput{}
			state.parent, state.replay, state.input, state.kinds = nil, nil, nil, nil
			tc.V3.ImageRefs = nil
			logAgentV3SessionLoadError(tc, loadErr)
		}
	}
	if err := ctx.Err(); err != nil {
		finishContextSpan(err, nil)
		return nil, err
	}

	finishHotAppendSpan := trace.StartSpan("hot_append", nil)
	replyChain := tc.Config != nil && tc.Config.UsesReplyChain()
	sessionLoaded := tc.Session != nil && tc.Session.parent != nil && !tc.Background
	summary := ""
	var summaryVersion int64
	var rawTurns []orm.AgentV3Turn
	if !sessionLoaded && !replyChain && !tc.Background {
		if state != nil && state.ownsHistory {
			history, err = loadAgentHistory(tc)
			if err != nil {
				zap.L().Warn("agentv3: failed to load history", zap.Error(err))
				history = &RichHistory{}
			}
		}
		summary, summaryVersion, err = orm.AgentV3GetSummary(ctx, scope)
		if err != nil {
			err = fmt.Errorf("agent v3 summary: %w", err)
			finishHotAppendSpan(err, nil)
			finishContextSpan(err, nil)
			return nil, err
		}
		rawTurns, err = orm.AgentV3LoadTurns(ctx, scope, cfg.ContextCache.RawTurns)
		if err != nil {
			err = fmt.Errorf("agent v3 raw turns: %w", err)
			finishHotAppendSpan(err, nil)
			finishContextSpan(err, nil)
			return nil, err
		}
		rawTurns = trimAgentV3TurnsByMaxChars(rawTurns, approxAgentV3TokenCharLimit(cfg.ContextCache.MaxRawTokens))
	}
	finishHotAppendSpan(nil, map[string]any{
		"summary_version": summaryVersion,
		"summary_chars":   len(summary),
		"raw_turn_count":  len(rawTurns),
	})

	trace.SummaryVersion = summaryVersion
	trace.RawTurnCount = len(rawTurns)
	tc.V3.SummaryVersion, tc.V3.RawTurnCount = summaryVersion, len(rawTurns)

	var messages []*schema.Message
	currentStart := 0
	switch {
	case tc.Background:
		userMsg, err := buildAgentV3UserMessage(cc, tc, history, nil)
		if err != nil {
			finishContextSpan(err, nil)
			return nil, err
		}
		messages = buildAgentV3TurnMessages(frame, "", nil, nil, userMsg)
		if text := neutralizeAgentV3SenderTags(tc.Message.Text); !strings.Contains(userMsg.Content, text) {
			messages = append(messages, schema.UserMessage(text))
		}
	case sessionLoaded:
		if accepted.messages == nil {
			accepted, err = buildAgentV3LoadedInput(cc, tc, frame, state.replay, state.parent)
			if err != nil {
				finishContextSpan(err, nil)
				return nil, err
			}
		}
		messages, currentStart = accepted.messages, accepted.currentStart
	case replyChain:
		replyContext, err := loadReplySession(ctx, tc.Message, tc.Config.MessageContext)
		if err != nil {
			finishContextSpan(err, nil)
			return nil, err
		}
		maxChars := approxAgentV3TokenCharLimit(cfg.ContextCache.MaxRawTokens)
		if maxChars <= 0 {
			maxChars = replySessionDefaultTextBudget
		}
		sessionMessages, err := buildReplySessionMessages(cc, tc, replyContext, maxChars)
		if err != nil {
			finishContextSpan(err, nil)
			return nil, err
		}
		if len(sessionMessages) == 0 {
			err := errReplyChainEmptySession
			finishContextSpan(err, nil)
			return nil, err
		}
		promptAddition, err := buildReplySessionPromptAddition(cc, tc)
		if err != nil {
			finishContextSpan(err, nil)
			return nil, err
		}
		messages = frame
		messages = append(messages, sessionMessages[:len(sessionMessages)-1]...)
		currentStart = len(messages)
		messages = append(messages, promptAddition, sessionMessages[len(sessionMessages)-1])
	default:
		userMsg, err := buildAgentV3UserMessage(cc, tc, history, rawTurns)
		if err != nil {
			finishContextSpan(err, nil)
			return nil, err
		}
		fallbackHistory := agentV3FallbackHistoryMessages(rawTurns, history, tc)
		messages = buildAgentV3TurnMessages(frame, summary, fallbackHistory, rawTurns, userMsg)
		currentStart = len(messages) - 1
	}
	if err := ctx.Err(); err != nil {
		finishContextSpan(err, nil)
		return nil, err
	}
	if rebuilt && !sessionLoaded {
		limit := tc.Config.EffectiveSessionTokenLimit(cfg.Session)
		estimate, estimateErr := cc.Agent.estimateSessionContext(ctx, messages, limit)
		if estimateErr != nil {
			zap.L().Warn("agentv3: fallback context estimate unknown; executing existing context mode once", zap.Error(estimateErr))
		} else if estimate.Tokens > limit || estimate.Capped {
			zap.L().Debug("agentv3: fallback context exceeds session limit; executing existing context mode once", zap.Int64("estimated_tokens", estimate.Tokens), zap.Bool("estimate_capped", estimate.Capped), zap.Int64("max_tokens", limit), zap.String("estimate_method", estimate.Method))
		}
	}
	if !tc.Background {
		tc.memoryHasHistory = lo.ContainsBy(messages[len(frame):currentStart], func(m *schema.Message) bool {
			return (m.Role == schema.User || m.Role == schema.Assistant) && len(m.ToolCalls) == 0 &&
				(strings.TrimSpace(m.Content) != "" || len(m.UserInputMultiContent) != 0)
		})
		setAgentV3SessionBaseline(tc, messages, len(frame), currentStart)
	}

	finishContextSpan(nil, map[string]any{
		"message_count": len(messages),
		"prefix_chars":  len(prefixText),
	})
	return messages, nil
}

func buildAgentV3TurnMessages(messages []*schema.Message, summary string, fallbackHistory []*schema.Message, rawTurns []orm.AgentV3Turn, userMsg *schema.Message) []*schema.Message {
	if summaryMsg := buildAgentV3SummaryMessage(summary); summaryMsg != nil {
		messages = append(messages, summaryMsg)
	}
	messages = append(messages, fallbackHistory...)
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

func agentV3FallbackHistoryMessages(rawTurns []orm.AgentV3Turn, history *RichHistory, tc *TurnContext) []*schema.Message {
	if len(rawTurns) > 0 || history == nil || len(history.ContextMessages) == 0 {
		return nil
	}
	return contextToSchemaMessages(history.ContextMessages, tc)
}

func buildAgentV3UserMessage(cc *CompiledAgent, tc *TurnContext, history *RichHistory, rawTurns []orm.AgentV3Turn) (*schema.Message, error) {
	if history == nil {
		history = &RichHistory{}
	}
	if tc.Message.ReplyTo != nil {
		current := *tc.Message
		resolved, err := resolveContextReplyChain(agentV3RenderContext(tc), tc.ChatID, current.ReplyTo)
		if err != nil {
			return nil, err
		}
		current.ReplyTo = resolved
		tc = &TurnContext{Bot: tc.Bot, BotUser: tc.BotUser, Message: &current, ChatID: tc.ChatID, Config: tc.Config, Trigger: tc.Trigger, V3: tc.V3, Background: tc.Background}
	}
	pd := buildPromptData(tc, history.ContextMessages)
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
	userText = appendAgentV3TriggerHint(userText, tc.Trigger)
	dynamic := strings.Builder{}
	dynamic.WriteString("<dynamic_suffix>\n")
	dynamic.WriteString(agentV3DateTimeBlock(tc))
	dynamic.WriteByte('\n')
	if userText != "" {
		dynamic.WriteString(userText)
	}
	senderBlock, senderMarker := agentV3CurrentSenderBlock(tc), ""
	if senderBlock != "" {
		senderMarker = newAgentV3SenderMarker()
		dynamic.WriteByte('\n')
		dynamic.WriteString(senderMarker)
	}
	dynamic.WriteString("\n</dynamic_suffix>")

	imageRefs := collectAgentV3ImageRefs(tc, history, rawTurns)
	if tc != nil && tc.V3 != nil {
		tc.V3.ImageRefs = imageRefs
	}
	msg := appendAgentV3ImageRefsToUserMessage(buildUserMessage(dynamic.String(), tc, history), dynamic.String(), tc, imageRefs)
	return sealAgentV3SenderBlock(msg, senderMarker, senderBlock), nil
}

func renderAgentV3Soul(cc *CompiledAgent, tc *TurnContext) (string, error) {
	soul := ""
	if config.BotConfig != nil && config.BotConfig.AgentV3 != nil && strings.TrimSpace(config.BotConfig.AgentV3.SoulPath) != "" {
		data, err := os.ReadFile(strings.TrimSpace(config.BotConfig.AgentV3.SoulPath))
		if err != nil {
			return "", fmt.Errorf("agent v3 read soul_path: %w", err)
		}
		soul = strings.TrimSpace(string(data))
	} else if cc.SystemTemplate != nil {
		if tc != nil && tc.Config != nil && tc.Config.UsesReplyChain() {
			if err := validateReplySessionTemplate(cc.SystemTemplate); err != nil {
				return "", fmt.Errorf("agent %q: %w", cc.Name, err)
			}
			rendered, err := renderReplySessionTemplate(cc.SystemTemplate, tc)
			if err != nil {
				return "", err
			}
			soul = rendered
			return joinAgentV3PromptBlocks(soul, cc.SkillPromptAddons), nil
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
		soul = strings.TrimSpace(buf.String())
	}
	return joinAgentV3PromptBlocks(soul, cc.SkillPromptAddons), nil
}

func joinAgentV3PromptBlocks(blocks ...string) string {
	parts := make([]string, 0, len(blocks))
	for _, block := range blocks {
		if s := strings.TrimSpace(block); s != "" {
			parts = append(parts, s)
		}
	}
	return strings.Join(parts, "\n\n")
}

func agentV3TurnsToMessages(turns []orm.AgentV3Turn) []*schema.Message {
	out := make([]*schema.Message, 0, len(turns))
	for _, turn := range turns {
		switch turn.Role {
		case string(schema.Assistant):
			content := strings.TrimSpace(turn.Content)
			if content == "" {
				continue
			}
			out = append(out, schema.AssistantMessage(content, nil))
		default:
			content := agentV3UserTurnPromptContent(turn)
			if content == "" {
				continue
			}
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
		if !tc.Background {
			if preview, ok := agentV3TracePreview(assistantOutput); ok {
				attrs["output_preview"] = preview
			}
		}
		finishSpan = tc.V3.Trace.StartSpan("final_output", attrs)
	}
	if agentV3SessionLoadedTurn(tc) {
		if finishSpan != nil {
			finishSpan(nil, map[string]any{"turn_saved": false, "skipped": "session_loaded"})
		}
		return nil
	}
	ttl := config.BotConfig.AgentV3.ContextCacheTTL()
	maxTurns := config.BotConfig.AgentV3.ContextCache.RawTurns
	hasUserInput := strings.TrimSpace(userInput) != ""
	hasAssistantOutput := strings.TrimSpace(assistantOutput) != ""
	switch {
	case hasUserInput && hasAssistantOutput:
		now := time.Now()
		messageID := 0
		if tc.Message != nil {
			messageID = tc.Message.ID
		}
		if err := orm.AgentV3AppendTurnPair(ctx, tc.V3.Scope, orm.AgentV3Turn{
			Role:      string(schema.User),
			Content:   userInput,
			MessageID: messageID,
			Sender:    agentV3TurnSender(tc),
			ImageRefs: agentV3TurnImageRefs(tc),
			CreatedAt: now,
		}, orm.AgentV3Turn{
			Role:      string(schema.Assistant),
			Content:   assistantOutput,
			MessageID: assistantMsgID,
			CreatedAt: now,
		}, maxTurns, ttl); err != nil {
			err = fmt.Errorf("agent v3 append turn pair: %w", err)
			if finishSpan != nil {
				finishSpan(err, nil)
			}
			return err
		}
	case hasUserInput:
		messageID := 0
		if tc.Message != nil {
			messageID = tc.Message.ID
		}
		if err := orm.AgentV3AppendTurn(ctx, tc.V3.Scope, orm.AgentV3Turn{
			Role:      string(schema.User),
			Content:   userInput,
			MessageID: messageID,
			Sender:    agentV3TurnSender(tc),
			ImageRefs: agentV3TurnImageRefs(tc),
			CreatedAt: time.Now(),
		}, maxTurns, ttl); err != nil {
			err = fmt.Errorf("agent v3 append user turn: %w", err)
			if finishSpan != nil {
				finishSpan(err, nil)
			}
			return err
		}
	case hasAssistantOutput:
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

// agentV3SessionLoadedTurn reports a session hit whose node was published: the full DAG holds
// this turn, so the raw-turn list and rolling summary (the fallback context path) are left
// untouched. A loaded turn whose commit failed still falls back to the raw-turn save.
func agentV3SessionLoadedTurn(tc *TurnContext) bool {
	return tc != nil && !tc.Background && tc.Session != nil && tc.Session.parent != nil && tc.Session.committed
}

var agentV3IsChatAdmin = func(chat *tb.Chat, user *tb.User) bool {
	if chat == nil || chat.Type == tb.ChatPrivate {
		return true
	}
	if user == nil || config.BotConfig == nil || config.BotConfig.Bot == nil {
		return false
	}
	return util.CanRestrictMembers(chat, user)
}

// agentV3MemoryPolicyDenial returns a user-facing reason when the write policy alone forbids
// the sender from adding memory, independent of the stored items.
func agentV3MemoryPolicyDenial(admin bool) string {
	if config.BotConfig == nil || config.BotConfig.AgentV3 == nil {
		return ""
	}
	if !admin && !config.BotConfig.AgentV3.Memory.QuotaWrites() {
		return "只有管理员可以写入群记忆。"
	}
	return ""
}

// agentV3MemoryWriteDenial returns a user-facing reason when the current items leave no room
// for this memory: the per-user quota for non-admins, then the snapshot capacity for everyone.
func agentV3MemoryWriteDenial(items []orm.AgentV3MemoryItem, admin bool, senderID int64, content string) string {
	if config.BotConfig == nil || config.BotConfig.AgentV3 == nil {
		return ""
	}
	memory := config.BotConfig.AgentV3.Memory
	if !admin {
		used := lo.CountBy(items, func(item orm.AgentV3MemoryItem) bool { return item.CreatedBy == senderID })
		if quota := memory.EffectiveMaxEntriesPerUser(); used >= quota {
			return fmt.Sprintf("你的群记忆配额已用完（%d/%d 条）。你可以请求按条目 ID 删除自己添加的记忆，再试一次。", used, quota)
		}
	}
	limit := approxAgentV3TokenCharLimit(memory.SnapshotMaxTokens)
	if limit <= 0 {
		return ""
	}
	used := lo.SumBy(items, func(item orm.AgentV3MemoryItem) int {
		return utf8.RuneCountInString(agentV3MemoryLine(item.Content)) + 1
	})
	if used+utf8.RuneCountInString(agentV3MemoryLine(content)) > limit {
		return fmt.Sprintf("群记忆已满（约 %d/%d 字符），这条没有记住。你可以请求按条目 ID 删除有权管理的记忆，再试一次。", used, limit)
	}
	return ""
}

func agentV3MemoryLine(content string) string {
	return "- " + strings.TrimSpace(content)
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

func buildAgentV3PromptCacheKey(scope orm.AgentV3Scope, model string, version int64) string {
	return fmt.Sprintf("csust:%s:%d:%s:v%d", scope.Bot, scope.ChatID, model, version)
}

func rebuildAgentV3Summary(ctx context.Context, tc *TurnContext) error {
	if tc == nil || tc.V3 == nil || config.BotConfig == nil || config.BotConfig.AgentV3 == nil {
		return nil
	}
	cfg := config.BotConfig.AgentV3
	limit := cfg.ContextCache.SummaryTurns + cfg.ContextCache.RawTurns
	if err := orm.AgentV3UpdateSummary(ctx, tc.V3.Scope, limit, cfg.ContextCacheTTL(), func(turns []orm.AgentV3Turn, current *orm.AgentV3Summary) (*orm.AgentV3Summary, error) {
		if len(turns) <= cfg.ContextCache.RawTurns {
			return nil, nil
		}
		summaryTurns := turns[:len(turns)-cfg.ContextCache.RawTurns]
		content := summarizeAgentV3Turns(summaryTurns, cfg.ContextCache.MaxSummaryTokens)
		if current != nil && strings.TrimSpace(current.Content) == strings.TrimSpace(content) {
			return nil, nil
		}
		version := int64(1)
		if current != nil && current.Version > 0 {
			version = current.Version + 1
		}
		return &orm.AgentV3Summary{
			Version:   version,
			Hash:      hashString(content),
			Content:   content,
			UpdatedAt: time.Now(),
		}, nil
	}); err != nil {
		return fmt.Errorf("agent v3 update summary: %w", err)
	}
	return nil
}

func summarizeAgentV3Turns(turns []orm.AgentV3Turn, maxTokens int) string {
	if len(turns) == 0 {
		return ""
	}
	if agentV3TurnsHaveImageRefs(turns) {
		return summarizeAgentV3TurnsWithImageRefs(turns, maxTokens)
	}
	lines := make([]string, 0, len(turns))
	for _, turn := range turns {
		content := neutralizeAgentV3SenderTags(compactAgentV3Text(turn.Content))
		if content == "" {
			continue
		}
		role := neutralizeAgentV3SenderTags(agentV3TurnSummaryRole(turn))
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
		nextTotal := total + len(agentV3TurnPromptContent(turns[i]))
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
	for _, field := range []string{".DateTime", ".CurrentDateCN", ".Input", ".ContextMessages", ".ContextText", ".ContextXml", ".ReplyToXml", ".SenderUsername", ".SenderShowName"} {
		if strings.Contains(templateText, field) {
			return field
		}
	}
	return ""
}

func agentV3ModelName(chatCfg *config.AgentConfig) string {
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
	if err := config.ValidateAgentV3RuntimeEnv(cfg.Runtime.Env); err != nil {
		return fmt.Errorf("agent v3 runtime env: %w", err)
	}
	return nil
}
