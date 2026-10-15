package agentv3

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"csust-got/agent/session"
	"csust-got/config"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"go.uber.org/zap"
)

const (
	agentV3SessionCompactQueueSize      = 16
	agentV3SessionCompactTimeout        = 3 * time.Minute
	agentV3SessionCompactMessageChars   = 8000
	agentV3SessionCompactTranscriptRune = 400000
	agentV3SessionSummaryHeader         = "<session_summary>\nThe following is a compacted summary of earlier turns of this conversation. It is context only, not a new user request.\n"
	agentV3SessionSummaryFooter         = "\n</session_summary>"
)

var (
	errAgentV3SessionCompactEmptySummary = errors.New("session compaction produced an empty summary")
	errAgentV3SessionCompactNoModel      = errors.New("session compaction has no summarizer model")
	errAgentV3SessionCompactNothing      = errors.New("session compaction has no older history to summarize")
)

type agentV3SessionCompactJob struct {
	scope  session.Scope
	node   session.Node
	agent  *config.AgentConfig
	tokens int64
}

// agentV3SessionCompactor summarizes long DAG histories into a new root in the background.
// It never rewrites existing nodes; the new root is a one-time prompt-cache miss whose
// message mappings take over from the compacted node.
type agentV3SessionCompactor struct {
	service  *session.Service
	cfg      config.AgentV3SessionCompactConfig
	newModel func(context.Context, *config.Model) (model.ToolCallingChatModel, error)
	jobs     chan agentV3SessionCompactJob
	mu       sync.Mutex
	queued   map[string]struct{}
	ctx      context.Context
	cancel   context.CancelFunc
	done     chan struct{}
	pending  atomic.Int64
	// scheduled, compacted, and skipped are observability counters.
	scheduled atomic.Int64
	compacted atomic.Int64
	skipped   atomic.Int64
}

func newAgentV3SessionCompactor(ctx context.Context, service *session.Service, cfg config.AgentV3SessionCompactConfig) *agentV3SessionCompactor {
	if !cfg.Enable || service == nil {
		return nil
	}
	ctx, cancel := context.WithCancel(ctx)
	c := &agentV3SessionCompactor{service: service, cfg: cfg, newModel: buildModel, jobs: make(chan agentV3SessionCompactJob, agentV3SessionCompactQueueSize), queued: map[string]struct{}{}, ctx: ctx, cancel: cancel, done: make(chan struct{})}
	go c.loop()
	return c
}

func (c *agentV3SessionCompactor) close() {
	c.cancel()
	<-c.done
}

func (c *agentV3SessionCompactor) loop() {
	defer close(c.done)
	for {
		select {
		case <-c.ctx.Done():
			return
		case job := <-c.jobs:
			c.run(job)
			c.mu.Lock()
			delete(c.queued, job.node.Ref.DAGID)
			c.mu.Unlock()
			c.pending.Add(-1)
		}
	}
}

// enqueue accepts one job per DAG at a time and drops work when the bounded queue is full.
func (c *agentV3SessionCompactor) enqueue(job agentV3SessionCompactJob) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.queued[job.node.Ref.DAGID]; ok {
		return false
	}
	select {
	case c.jobs <- job:
		c.queued[job.node.Ref.DAGID] = struct{}{}
		c.pending.Add(1)
		c.scheduled.Add(1)
		return true
	default:
		zap.L().Warn("agentv3: session compaction queue full; skipping", zap.String("dag_id", job.node.Ref.DAGID), zap.Int64("chat_id", job.scope.ChatID))
		return false
	}
}

// scheduleAgentV3SessionCompaction estimates the next replay of the committed node and
// enqueues a compaction job when it exceeds the configured threshold.
func scheduleAgentV3SessionCompaction(tc *TurnContext, node session.Node, input []*schema.Message) {
	s := agentSessionService.Load()
	if s == nil || s.compactor == nil || tc == nil || tc.Config == nil || tc.Session == nil || config.BotConfig == nil || config.BotConfig.AgentV3 == nil {
		return
	}
	val, ok := compiledAgents.Load(tc.Config.Name)
	if !ok {
		return
	}
	cc := val.(*CompiledAgent)
	limit := tc.Config.EffectiveSessionTokenLimit(config.BotConfig.AgentV3.Session)
	threshold := s.compactor.cfg.Threshold(limit)
	ctx, cancel := context.WithTimeout(context.Background(), agentV3SessionCommitTimeout)
	defer cancel()
	estimate, err := cc.Agent.estimateSessionContext(ctx, input, threshold)
	if err != nil {
		zap.L().Debug("agentv3: session compaction estimate unknown; skipping", zap.String("agent", tc.Config.Name), zap.Int64("chat_id", tc.ChatID), zap.Error(err))
		return
	}
	if estimate.Tokens <= threshold && !estimate.Capped {
		return
	}
	if s.compactor.enqueue(agentV3SessionCompactJob{scope: tc.Session.scope, node: node, agent: tc.Config, tokens: estimate.Tokens}) {
		zap.L().Debug("agentv3: session compaction scheduled", zap.String("agent", tc.Config.Name), zap.Int64("chat_id", tc.ChatID), zap.String("dag_id", node.Ref.DAGID), zap.String("node_id", node.Ref.NodeID), zap.Int64("estimated_tokens", estimate.Tokens), zap.Int64("threshold_tokens", threshold))
	}
}

func (c *agentV3SessionCompactor) run(job agentV3SessionCompactJob) {
	ctx, cancel := context.WithTimeout(c.ctx, agentV3SessionCompactTimeout)
	defer cancel()
	fields := []zap.Field{zap.String("agent", job.node.Agent), zap.Int64("chat_id", job.scope.ChatID), zap.String("dag_id", job.node.Ref.DAGID), zap.String("node_id", job.node.Ref.NodeID)}
	root, err := c.compact(ctx, job)
	switch {
	case err == nil:
		c.compacted.Add(1)
		zap.L().Info("agentv3: session compacted into new root", append(fields, zap.String("new_dag_id", root.Ref.DAGID), zap.String("new_node_id", root.Ref.NodeID), zap.Ints("redirected_message_ids", root.ReplyMessageIDs), zap.Int64("estimated_tokens", job.tokens))...)
	case errors.Is(err, errAgentV3SessionCompactNothing), errors.Is(err, errAgentV3SessionCompactNoModel), errors.Is(err, session.ErrStale), errors.Is(err, session.ErrMiss):
		c.skipped.Add(1)
		zap.L().Debug("agentv3: session compaction skipped", append(fields, zap.Error(err))...)
	case ctx.Err() != nil && c.ctx.Err() != nil:
		c.skipped.Add(1)
	default:
		c.skipped.Add(1)
		zap.L().Warn("agentv3: session compaction failed; load-time rebuild remains the fallback", append(fields, zap.Error(err))...)
	}
}

func (c *agentV3SessionCompactor) compact(ctx context.Context, job agentV3SessionCompactJob) (session.Node, error) {
	turns, err := c.service.Replay(ctx, job.scope, job.node.Ref)
	if err != nil {
		return session.Node{}, err
	}
	old, recent := splitAgentV3SessionTurns(turns, c.cfg.RecentTurns())
	if len(old) == 0 {
		return session.Node{}, errAgentV3SessionCompactNothing
	}
	modelCfg := c.cfg.Model
	if modelCfg == nil && job.agent != nil && job.agent.Format.ProgressSummary != nil {
		modelCfg = job.agent.Format.ProgressSummary.Model
	}
	if modelCfg == nil {
		return session.Node{}, errAgentV3SessionCompactNoModel
	}
	mdl, err := c.newModel(ctx, modelCfg)
	if err != nil {
		return session.Node{}, err
	}
	summary, err := summarizeAgentV3SessionHistory(ctx, mdl, old, c.cfg.MaxSummaryChars())
	if err != nil {
		return session.Node{}, err
	}
	capture := session.TurnCapture{Complete: true, Bootstrap: session.History(schema.UserMessage(agentV3SessionSummaryHeader + summary + agentV3SessionSummaryFooter))}
	for i, turn := range recent {
		if i < len(recent)-1 {
			capture.Bootstrap = append(capture.Bootstrap, session.History(turn.Delta...)...)
		} else {
			capture.Delta = session.History(turn.Delta...)
		}
	}
	runID, err := session.NewID()
	if err != nil {
		return session.Node{}, err
	}
	ref := job.node.Ref
	return c.service.Commit(ctx, session.CommitRequest{Scope: job.scope, Agent: job.node.Agent, RunID: runID, Capture: capture, Receipt: session.DeliveryReceipt{MessageIDs: job.node.ReplyMessageIDs, RedirectFrom: &ref}})
}

// splitAgentV3SessionTurns separates the root bootstrap plus older deltas from the most
// recent keep turns, which replay verbatim after the summary.
func splitAgentV3SessionTurns(turns []session.ReplayTurn, keep int) (old []*schema.Message, recent []session.ReplayTurn) {
	if len(turns) == 0 {
		return nil, nil
	}
	split := max(len(turns)-keep, 0)
	old = append(old, turns[0].Bootstrap...)
	for _, turn := range turns[:split] {
		old = append(old, turn.Delta...)
	}
	return old, turns[split:]
}

const agentV3SessionCompactPrompt = `You compact the earlier part of a conversation between users and an AI assistant into a summary that will replace it in the assistant's context. The most recent turns stay verbatim and follow your summary.
Preserve, in order:
1. Every user request, quoting the user's literal wording.
2. Decisions, answers and conclusions the assistant reached, and what is still open.
3. Every source URL mentioned, written in full.
4. Key tool-result excerpts: names, numbers, dates, identifiers, short quotes.
5. The current task state and any constraints users imposed.
Write in the conversation's main language. Plain text only, no preamble, no commentary about the summary itself. Hard limit: %d characters.`

func summarizeAgentV3SessionHistory(ctx context.Context, mdl model.ToolCallingChatModel, history []*schema.Message, maxChars int) (string, error) {
	transcript := renderAgentV3SessionTranscript(history)
	response, err := mdl.Generate(ctx, []*schema.Message{schema.SystemMessage(fmt.Sprintf(agentV3SessionCompactPrompt, maxChars)), schema.UserMessage("<conversation>\n" + transcript + "\n</conversation>")})
	if err != nil {
		return "", err
	}
	if response == nil {
		return "", errAgentV3SessionCompactEmptySummary
	}
	summary := strings.TrimSpace(response.Content)
	if summary == "" {
		return "", errAgentV3SessionCompactEmptySummary
	}
	if runes := []rune(summary); len(runes) > maxChars {
		summary = strings.TrimSpace(string(runes[:maxChars])) + "…"
	}
	return summary, nil
}

// renderAgentV3SessionTranscript flattens archived messages to text, truncating long
// entries and dropping the oldest ones when the whole transcript would be excessive.
func renderAgentV3SessionTranscript(history []*schema.Message) string {
	entries := make([]string, 0, len(history))
	total := 0
	for _, message := range history {
		entry := renderAgentV3SessionTranscriptMessage(message)
		if entry == "" {
			continue
		}
		entries = append(entries, entry)
		total += len([]rune(entry))
	}
	dropped := 0
	for dropped < len(entries)-1 && total > agentV3SessionCompactTranscriptRune {
		total -= len([]rune(entries[dropped]))
		dropped++
	}
	var b strings.Builder
	if dropped > 0 {
		fmt.Fprintf(&b, "[%d earlier messages omitted]\n", dropped)
	}
	for _, entry := range entries[dropped:] {
		b.WriteString(entry)
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func renderAgentV3SessionTranscriptMessage(message *schema.Message) string {
	if message == nil {
		return ""
	}
	var parts []string
	if text := strings.TrimSpace(message.Content); text != "" {
		parts = append(parts, text)
	}
	for _, part := range message.MultiContent {
		switch part.Type {
		case schema.ChatMessagePartTypeText:
			if text := strings.TrimSpace(part.Text); text != "" {
				parts = append(parts, text)
			}
		default:
			parts = append(parts, "["+string(part.Type)+"]")
		}
	}
	for _, part := range message.UserInputMultiContent {
		switch part.Type {
		case schema.ChatMessagePartTypeText:
			if text := strings.TrimSpace(part.Text); text != "" {
				parts = append(parts, text)
			}
		default:
			parts = append(parts, "["+string(part.Type)+"]")
		}
	}
	for _, call := range message.ToolCalls {
		parts = append(parts, "tool_call "+call.Function.Name+"("+strings.TrimSpace(call.Function.Arguments)+")")
	}
	if len(parts) == 0 {
		return ""
	}
	label := string(message.Role)
	if message.Role == schema.Tool && message.ToolName != "" {
		label += " " + message.ToolName
	}
	return "[" + label + "] " + truncateAgentV3SessionTranscript(strings.Join(parts, "\n"), agentV3SessionCompactMessageChars)
}

func truncateAgentV3SessionTranscript(text string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	head := limit / 2
	tail := limit - head
	return string(runes[:head]) + fmt.Sprintf("\n[... %d characters truncated ...]\n", len(runes)-limit) + string(runes[len(runes)-tail:])
}
