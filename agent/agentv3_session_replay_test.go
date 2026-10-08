package agentv3

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"csust-got/agent/session"
	"csust-got/config"
	"csust-got/orm"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	"github.com/stretchr/testify/require"
	tb "gopkg.in/telebot.v3"
)

// The replayed branch input must start with the exact message sequence the root turn last
// sent to the model so provider prompt caches can reuse the archived prefix.
func TestAgentV3SessionReplayPrefixMatchesRootModelInput(t *testing.T) {
	f := newAgentSessionFixture(t)
	cfg := &config.AgentConfig{Name: "aligned", ContextMode: "reply_chain"}
	mdl := &scriptedToolModel{turns: [][]*schema.Message{{sessionToolMessage("align-call")}, {schema.AssistantMessage("ROOT_FINAL", nil)}, {schema.AssistantMessage("BRANCH_FINAL", nil)}}}
	f.compile(t, cfg, mdl, lookupTool{})
	first := f.chat(t, cfg, sessionMessage(100, 7, 0, "root input"), nil)
	rootInputs := mdl.capturedInputs()
	require.Len(t, rootInputs, 2)
	lastRootInput := rootInputs[1]
	require.Contains(t, lastRootInput[len(lastRootInput)-1].Content, "<agent_runtime_guidance>", "the root turn ends with runtime guidance before its final call")
	current := sessionMessage(200, 7, 60, "branch input")
	current.ReplyTo = &tb.Message{ID: first}
	f.chat(t, cfg, current, &config.AgentTrigger{Reply: true})
	branchInput := mdl.capturedInputs()[2]
	require.Greater(t, len(branchInput), len(lastRootInput))
	require.Equal(t, lastRootInput, branchInput[:len(lastRootInput)], "system, template addition, tool round and guidance replay verbatim")
	require.Equal(t, "ROOT_FINAL", branchInput[len(lastRootInput)].Content)
	require.Contains(t, replySessionSchemaText(branchInput[len(lastRootInput)+1:]), "branch input")
	archive := f.archive(t, f.node(t, first))
	require.Len(t, archive.Frame, 1)
	require.Equal(t, schema.System, archive.Frame[0].Message.Role)
	require.Equal(t, lastRootInput[1:], sessionRecordMessages(archive.Delta)[:len(lastRootInput)-1])
}

func TestAgentV3SessionArchiveKeepsGuidanceInlineAsHistory(t *testing.T) {
	mdl := &scriptedToolModel{turns: [][]*schema.Message{
		{{Role: schema.Assistant, ToolCalls: []schema.ToolCall{lookupToolCall("call", `{}`)}}},
		{schema.AssistantMessage("final answer", nil)},
	}}
	agent := newSessionCaptureAgent(t, mdl, []tool.BaseTool{lookupTool{}}, 2)
	capture := NewSessionCapture()
	input := []*schema.Message{schema.SystemMessage("system"), schema.UserMessage("input")}
	_, err := agent.Generate(WithSessionCapture(t.Context(), capture), input)
	require.NoError(t, err)
	snapshot := capture.Snapshot()
	require.NoError(t, snapshot.Err)
	require.True(t, snapshot.Complete)
	require.Len(t, snapshot.Guidance, 1)
	require.Equal(t, 2, snapshot.Guidance[0].Index)
	archive, err := agentV3SessionArchive(&agentV3SessionTurn{input: input, kinds: []agentV3SessionInputKind{agentV3SessionFrame, agentV3SessionCurrent}}, snapshot)
	require.NoError(t, err)
	require.Len(t, archive.Frame, 1)
	require.Equal(t, schema.System, archive.Frame[0].Message.Role)
	require.Len(t, archive.Delta, 5)
	for _, record := range archive.Delta {
		require.Equal(t, session.SourceHistory, record.Source)
	}
	require.Equal(t, "input", archive.Delta[0].Message.Content)
	require.Equal(t, "call", archive.Delta[2].Message.ToolCallID)
	require.Contains(t, archive.Delta[3].Message.Content, finalTurnGuidance)
	require.Equal(t, "final answer", archive.Delta[4].Message.Content)
	require.NoError(t, session.ValidateCapture(archive, true))
	replayed, err := session.Snapshot(archive)
	require.NoError(t, err)
	require.Len(t, replayed.Delta, 5)
}

func TestAgentV3SessionLoadedInputReplaysVerbatimAndSupersedesMemory(t *testing.T) {
	f := newAgentSessionFixture(t)
	cfg := &config.AgentConfig{Name: "memory-dedupe", ContextMode: "reply_chain"}
	compiled := f.compile(t, cfg, &scriptedToolModel{})
	current := sessionMessage(1100, 8, 60, "CURRENT")
	tc := &TurnContext{Bot: f.bot, BotUser: f.bot.Me, Config: cfg, Message: current, ChatID: -100}
	common, err := prepareAgentV3Turn(WithTurnContext(t.Context(), tc), compiled, tc, nil)
	require.NoError(t, err)
	memory := "- remembered fact"
	snapshot := buildAgentV3MemorySnapshotMessage(memory)
	replay := []*schema.Message{snapshot, schema.UserMessage("<reply_session_metadata>OLD_ADDITION</reply_session_metadata>"), schema.UserMessage("old"), schema.AssistantMessage("answer", nil)}
	count := func(messages []*schema.Message) int {
		return strings.Count(replySessionSchemaText(messages), "<group_memory_snapshot")
	}

	prepared, err := buildAgentV3LoadedInput(compiled, tc, common[0].Content, memory, replay, nil)
	require.NoError(t, err)
	require.Equal(t, []int{0}, prepared.frameIndexes, "only the system message is a frame")
	require.Equal(t, replay, prepared.messages[1:1+len(replay)], "history including old addition and memory replays verbatim")
	require.Equal(t, 1+len(replay), prepared.currentStart)
	require.Equal(t, 1, count(prepared.messages), "an unchanged memory snapshot is not repeated")
	require.Contains(t, replySessionSchemaText(prepared.messages[prepared.currentStart:]), "CURRENT")

	prepared, err = buildAgentV3LoadedInput(compiled, tc, common[0].Content, "- changed fact", replay, nil)
	require.NoError(t, err)
	require.Equal(t, 2, count(prepared.messages), "a changed memory snapshot is appended after the replay")
	require.Equal(t, 1+len(replay), prepared.currentStart, "the fresh snapshot belongs to this turn's delta")
	require.Equal(t, replay, prepared.messages[1:1+len(replay)], "the stale snapshot is never rewritten")
	appended := prepared.messages[prepared.currentStart]
	require.True(t, strings.HasPrefix(appended.Content, agentV3MemorySnapshotSupersedeHeader), "the appended snapshot declares that it supersedes earlier ones")
	require.Contains(t, appended.Content, "- changed fact")

	prepared, err = buildAgentV3LoadedInput(compiled, tc, common[0].Content, "", replay, nil)
	require.NoError(t, err)
	require.Equal(t, 2, count(prepared.messages), "cleared memory appends an explicit marker")
	require.Equal(t, agentV3MemorySnapshotCleared, prepared.messages[prepared.currentStart].Content)
	require.Contains(t, prepared.messages[prepared.currentStart].Content, "群记忆已清空")

	prepared, err = buildAgentV3LoadedInput(compiled, tc, common[0].Content, "", []*schema.Message{schema.UserMessage("old"), schema.AssistantMessage("answer", nil)}, nil)
	require.NoError(t, err)
	require.Zero(t, count(prepared.messages), "no memory and no earlier snapshot appends nothing")

	cleared := []*schema.Message{snapshot, schema.UserMessage("a"), schema.AssistantMessage("b", nil), schema.UserMessage(agentV3MemorySnapshotCleared), schema.UserMessage("c"), schema.AssistantMessage("d", nil)}
	prepared, err = buildAgentV3LoadedInput(compiled, tc, common[0].Content, "", cleared, nil)
	require.NoError(t, err)
	require.Equal(t, 2, count(prepared.messages), "an already cleared history gets no second marker")
	prepared, err = buildAgentV3LoadedInput(compiled, tc, common[0].Content, memory, cleared, nil)
	require.NoError(t, err)
	require.Equal(t, 3, count(prepared.messages), "memory restored after a clear is appended as superseding")
	require.True(t, strings.HasPrefix(prepared.messages[prepared.currentStart].Content, agentV3MemorySnapshotSupersedeHeader))

	older := []*schema.Message{buildAgentV3MemorySnapshotMessage("- stale"), schema.UserMessage("a"), schema.AssistantMessage("b", nil), snapshot, schema.UserMessage("c"), schema.AssistantMessage("d", nil)}
	prepared, err = buildAgentV3LoadedInput(compiled, tc, common[0].Content, memory, older, nil)
	require.NoError(t, err)
	require.Equal(t, 2, count(prepared.messages), "comparison uses the latest archived snapshot")
	superseded := []*schema.Message{snapshot, schema.UserMessage("a"), buildAgentV3MemorySnapshotUpdate("- second"), schema.UserMessage("c")}
	require.Nil(t, agentV3MemorySnapshotDelta(superseded, "- second"), "a superseding snapshot counts as the latest state")

	tests := []struct {
		name    string
		message *schema.Message
		body    string
		found   bool
	}{
		{name: "nil", message: nil},
		{name: "plain", message: snapshot, body: memory, found: true},
		{name: "superseding", message: buildAgentV3MemorySnapshotUpdate("- next"), body: "- next", found: true},
		{name: "cleared", message: schema.UserMessage(agentV3MemorySnapshotCleared), body: "", found: true},
		{name: "assistant role", message: &schema.Message{Role: schema.Assistant, Content: snapshot.Content}},
		{name: "look-alike user text", message: schema.UserMessage("<group_memory_snapshot>REAL_USER")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body, found := agentV3MemorySnapshotBody(tt.message)
			require.Equal(t, tt.found, found)
			require.Equal(t, tt.body, body)
		})
	}
}

type agentSessionPublishFailureRepository struct {
	session.Repository
	fail atomic.Bool
}

func (r *agentSessionPublishFailureRepository) Publish(ctx context.Context, scope session.Scope, intent session.Intent, digest string, size int64, receipt session.DeliveryReceipt) (session.Node, error) {
	if r.fail.Load() {
		return session.Node{}, errAgentSessionPublishFailure
	}
	return r.Repository.Publish(ctx, scope, intent, digest, size, receipt)
}

var errAgentSessionPublishFailure = errors.New("fixture publish failure")

func TestAgentV3SessionLoadedTurnFallsBackToRawTurnsWhenCommitFails(t *testing.T) {
	for _, failPublish := range []bool{false, true} {
		t.Run(fmt.Sprintf("publish_fails=%t", failPublish), func(t *testing.T) {
			f := newAgentSessionFixture(t)
			require.NoError(t, f.service.Close())
			files, err := session.NewFileStore(f.directory)
			require.NoError(t, err)
			repo := &agentSessionPublishFailureRepository{Repository: f.repo}
			f.service, err = session.NewService(repo, files, session.Options{TTL: time.Hour})
			require.NoError(t, err)
			f.files = files
			done := make(chan struct{})
			close(done)
			agentSessionService.Store(&agentV3SessionService{service: f.service, cancel: func() {}, done: done})

			cfg := &config.AgentConfig{Name: "commit-fallback", ContextMode: "chat", Session: config.AgentSessionConfig{LoadContext: true}}
			mdl := &scriptedToolModel{turns: [][]*schema.Message{{schema.AssistantMessage("ROOT_FINAL", nil)}, {schema.AssistantMessage("CHILD_FINAL", nil)}, {schema.AssistantMessage("NEXT_FINAL", nil)}}}
			f.compile(t, cfg, mdl)
			scope := orm.AgentV3Scope{Bot: f.scope().Bot, Platform: agentV3Platform, ChatID: -100}
			first := f.chat(t, cfg, sessionMessage(10, 7, 0, "ROOT_INPUT"), nil)
			require.Nil(t, f.node(t, first).Parent)
			turns, err := orm.AgentV3LoadTurns(t.Context(), scope, 12)
			require.NoError(t, err)
			require.Len(t, turns, 2, "a new root always keeps the fallback raw turns")

			repo.fail.Store(failPublish)
			second := f.chat(t, cfg, sessionMessage(20, 7, 60, "CHILD_INPUT"), nil)
			turns, err = orm.AgentV3LoadTurns(t.Context(), scope, 12)
			require.NoError(t, err)
			text := replySessionSchemaText(mdl.capturedInputs()[1])
			require.Contains(t, text, "ROOT_FINAL", "the second turn loaded the root as its parent")
			if failPublish {
				require.Len(t, turns, 4, "a loaded turn whose publish failed must still reach the raw-turn fallback")
				require.Equal(t, "CHILD_INPUT", turns[2].Content)
				require.Equal(t, "CHILD_FINAL", turns[3].Content)
				_, err = f.service.Load(t.Context(), session.Selection{Scope: f.scope(), Mode: session.SelectReply, ReplyMessageID: second})
				require.ErrorIs(t, err, session.ErrMiss)

				repo.fail.Store(false)
				next := f.chat(t, cfg, sessionMessage(30, 7, 120, "NEXT_INPUT"), nil)
				text = replySessionSchemaText(mdl.capturedInputs()[2])
				require.Contains(t, text, "CHILD_INPUT", "the next latest selection misses and the legacy context carries the failed turn")
				require.Contains(t, text, "CHILD_FINAL")
				require.Nil(t, f.node(t, next).Parent)
				loaded, err := f.service.Load(t.Context(), session.Selection{Scope: f.scope(), Mode: session.SelectReply, ReplyMessageID: first})
				require.NoError(t, err, "replying to the dropped latest parent still loads it")
				require.NoError(t, loaded.Parent.Close())
			} else {
				require.Len(t, turns, 2, "a published session hit skips the raw-turn fallback")
				root := f.node(t, first)
				require.Equal(t, &root.Ref, f.node(t, second).Parent)
				next := f.chat(t, cfg, sessionMessage(30, 7, 120, "NEXT_INPUT"), nil)
				child := f.node(t, second)
				require.Equal(t, &child.Ref, f.node(t, next).Parent)
			}
		})
	}
}

func TestAgentV3DeliveredMessagesFallback(t *testing.T) {
	last := &tb.Message{ID: 7}
	all := []*tb.Message{{ID: 1}, {ID: 2}}
	require.Equal(t, all, agentV3DeliveredMessages(all, last))
	require.Equal(t, []*tb.Message{last}, agentV3DeliveredMessages(nil, last))
	require.Nil(t, agentV3DeliveredMessages(nil, nil))
}

func TestAgentV3SessionCommitIndexesEveryDeliveredChunk(t *testing.T) {
	f := newAgentSessionFixture(t)
	cfg := &config.AgentConfig{Name: "chunks", ContextMode: "reply_chain"}
	compiled := f.compile(t, cfg, &scriptedToolModel{turns: [][]*schema.Message{{schema.AssistantMessage("LONG_FINAL", nil)}}})
	tc := &TurnContext{Bot: f.bot, BotUser: f.bot.Me, Message: sessionMessage(100, 7, 0, "chunked input"), ChatID: -100, Config: cfg}
	ctx := WithTurnContext(t.Context(), tc)
	setupAgentV3SessionTurn(tc)
	defer closeAgentV3SessionTurn(tc)
	messages, err := prepareAgentV3Turn(ctx, compiled, tc, nil)
	require.NoError(t, err)
	_, err = compiled.Agent.Generate(WithSessionCapture(ctx, tc.Session.capture), messages)
	require.NoError(t, err)
	require.False(t, tc.Session.committed)
	require.True(t, commitAgentV3Session(tc, []*tb.Message{{ID: 501}, nil, {ID: 502}, {ID: 0}, {ID: 503}}))
	require.True(t, tc.Session.committed)
	require.False(t, commitAgentV3Session(tc, nil), "no delivered ID means nothing was published")
	node := f.node(t, 503)
	require.Equal(t, []int{501, 502, 503}, node.ReplyMessageIDs, "invalid entries are skipped; the first ID stays primary")
	require.Equal(t, node, f.node(t, 501))
	require.Equal(t, node, f.node(t, 502))
	loaded, err := f.service.Load(t.Context(), session.Selection{Scope: f.scope(), Mode: session.SelectReply, ReplyMessageID: 503})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, loaded.Parent.Close()) })
	require.Equal(t, node.Ref, loaded.Parent.Ref())
	require.True(t, loaded.Parent.ContainsReplyMessageID(501))
	require.True(t, loaded.Parent.ContainsReplyMessageID(503))
	require.Contains(t, replySessionSchemaText(loaded.Messages), "LONG_FINAL")
	_, err = f.service.Load(t.Context(), session.Selection{Scope: f.scope(), Mode: session.SelectReply, ReplyMessageID: 504})
	require.ErrorIs(t, err, session.ErrMiss)
}

func TestAgentV3PromptCacheKeyIgnoresAgentName(t *testing.T) {
	scope := orm.AgentV3Scope{Bot: "sessionbot", Platform: agentV3Platform, ChatID: -100}
	key := buildAgentV3PromptCacheKey(scope, "model-a", 3)
	require.Equal(t, "csust:sessionbot:-100:model-a:v3", key)
	require.NotContains(t, key, "agent")
}
