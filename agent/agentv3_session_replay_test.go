package agentv3

import (
	"strings"
	"testing"

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

func TestAgentV3SessionLoadedInputReplaysVerbatimAndDeduplicatesMemory(t *testing.T) {
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
		return strings.Count(replySessionSchemaText(messages), agentV3MemorySnapshotHeader)
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
	require.Contains(t, prepared.messages[prepared.currentStart].Content, "- changed fact")

	prepared, err = buildAgentV3LoadedInput(compiled, tc, common[0].Content, "", replay, nil)
	require.NoError(t, err)
	require.Equal(t, 1, count(prepared.messages), "no memory means nothing new is appended")

	older := []*schema.Message{buildAgentV3MemorySnapshotMessage("- stale"), schema.UserMessage("a"), schema.AssistantMessage("b", nil), snapshot, schema.UserMessage("c"), schema.AssistantMessage("d", nil)}
	prepared, err = buildAgentV3LoadedInput(compiled, tc, common[0].Content, memory, older, nil)
	require.NoError(t, err)
	require.Equal(t, 2, count(prepared.messages), "comparison uses the latest archived snapshot")

	require.False(t, agentV3ReplayHasMemorySnapshot(nil, snapshot))
	require.False(t, agentV3ReplayHasMemorySnapshot([]*schema.Message{schema.UserMessage(agentV3MemorySnapshotHeader + "other\n</group_memory_snapshot>")}, snapshot))
	require.False(t, agentV3ReplayHasMemorySnapshot([]*schema.Message{{Role: schema.Assistant, Content: snapshot.Content}}, snapshot), "only user-role snapshots count")
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
	commitAgentV3Session(tc, []*tb.Message{{ID: 501}, nil, {ID: 502}, {ID: 0}, {ID: 503}})
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
