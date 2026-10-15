package agentv3

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	"csust-got/agent/session"
	"csust-got/config"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"github.com/stretchr/testify/require"
	tb "gopkg.in/telebot.v3"
)

const compactSummaryText = "SUMMARY_TEXT: the user asked FIRST_INPUT and the assistant answered FIRST_FINAL"

func newCompactTestModel() *scriptedToolModel {
	return &scriptedToolModel{turns: [][]*schema.Message{{schema.AssistantMessage(compactSummaryText, nil)}}}
}

func attachCompactor(t *testing.T, f *agentSessionFixture, cfg config.AgentV3SessionCompactConfig, summarizer model.ToolCallingChatModel) *agentV3SessionCompactor {
	t.Helper()
	if cfg.Model == nil {
		cfg.Model = &config.Model{Model: "fake-summarizer"}
	}
	config.BotConfig.AgentV3.Session.Compact = cfg
	c := newAgentV3SessionCompactor(t.Context(), f.service, cfg)
	if c != nil {
		c.newModel = func(context.Context, *config.Model) (model.ToolCallingChatModel, error) { return summarizer, nil }
		t.Cleanup(c.close)
	}
	agentSessionService.Load().compactor = c
	return c
}

func waitCompactorIdle(t *testing.T, c *agentV3SessionCompactor) {
	t.Helper()
	require.Eventually(t, func() bool { return c.pending.Load() == 0 }, 10*time.Second, 10*time.Millisecond)
}

func compactFixtureMessages(t *testing.T, f *agentSessionFixture) map[string]session.NodeRef {
	t.Helper()
	s := f.scope()
	key := config.BotConfig.RedisConfig.KeyPrefix + "agentv3:session:{" + s.Namespace + "}:scope:" + s.Key() + ":messages"
	raw, err := f.client.HGetAll(t.Context(), key).Result()
	require.NoError(t, err)
	out := map[string]session.NodeRef{}
	for id, value := range raw {
		var ref session.NodeRef
		require.NoError(t, json.Unmarshal([]byte(value), &ref))
		out[id] = ref
	}
	return out
}

func compactChatChain(t *testing.T, f *agentSessionFixture, cfg *config.AgentConfig, c *agentV3SessionCompactor) (first, second, third int) {
	t.Helper()
	first = f.chat(t, cfg, sessionMessage(10, 7, 0, "FIRST_INPUT"), &config.AgentTrigger{Command: "ask"})
	if c != nil {
		waitCompactorIdle(t, c)
	}
	secondMessage := sessionMessage(20, 8, 60, "SECOND_INPUT")
	secondMessage.ReplyTo = &tb.Message{ID: first}
	second = f.chat(t, cfg, secondMessage, &config.AgentTrigger{Reply: true})
	if c != nil {
		waitCompactorIdle(t, c)
	}
	thirdMessage := sessionMessage(30, 9, 120, "THIRD_INPUT")
	thirdMessage.ReplyTo = &tb.Message{ID: second}
	third = f.chat(t, cfg, thirdMessage, &config.AgentTrigger{Reply: true})
	if c != nil {
		waitCompactorIdle(t, c)
	}
	return first, second, third
}

func TestAgentV3SessionCompactionBelowThresholdSchedulesNothing(t *testing.T) {
	f := newAgentSessionFixture(t)
	cfg := &config.AgentConfig{Name: "quiet", ContextMode: "reply_chain"}
	f.compile(t, cfg, &scriptedToolModel{turns: [][]*schema.Message{{schema.AssistantMessage("one", nil)}, {schema.AssistantMessage("two", nil)}, {schema.AssistantMessage("three", nil)}}})
	summarizer := newCompactTestModel()
	c := attachCompactor(t, f, config.AgentV3SessionCompactConfig{Enable: true, ThresholdTokens: 10_000_000, KeepRecentTurns: 1}, summarizer)
	require.NotNil(t, c)
	_, _, third := compactChatChain(t, f, cfg, c)
	require.Zero(t, c.scheduled.Load())
	require.Empty(t, summarizer.capturedInputs())
	require.NotNil(t, f.node(t, third).Parent, "the delivered node is still the uncompacted child")
}

func TestAgentV3SessionCompactionDisabledCreatesNoCompactor(t *testing.T) {
	f := newAgentSessionFixture(t)
	cfg := &config.AgentConfig{Name: "disabled", ContextMode: "reply_chain"}
	f.compile(t, cfg, &scriptedToolModel{turns: [][]*schema.Message{{schema.AssistantMessage("one", nil)}, {schema.AssistantMessage("two", nil)}, {schema.AssistantMessage("three", nil)}}})
	summarizer := newCompactTestModel()
	require.Nil(t, attachCompactor(t, f, config.AgentV3SessionCompactConfig{ThresholdTokens: 1}, summarizer))
	first, _, third := compactChatChain(t, f, cfg, nil)
	require.Empty(t, summarizer.capturedInputs())
	require.Equal(t, f.node(t, first).Ref.DAGID, f.node(t, third).Ref.DAGID)
	require.Nil(t, f.node(t, third).RedirectedFrom)
}

func TestAgentV3SessionCompactionWritesNewRootAndRedirectsReplies(t *testing.T) {
	f := newAgentSessionFixture(t)
	cfg := &config.AgentConfig{Name: "compact", ContextMode: "reply_chain"}
	mdl := &scriptedToolModel{turns: [][]*schema.Message{{schema.AssistantMessage("FIRST_FINAL", nil)}, {schema.AssistantMessage("SECOND_FINAL", nil)}, {schema.AssistantMessage("THIRD_FINAL", nil)}, {schema.AssistantMessage("FOURTH_FINAL", nil)}}}
	f.compile(t, cfg, mdl)
	summarizer := newCompactTestModel()
	c := attachCompactor(t, f, config.AgentV3SessionCompactConfig{Enable: true, ThresholdTokens: 1, KeepRecentTurns: 2, SummaryMaxChars: 500}, summarizer)
	first, second, third := compactChatChain(t, f, cfg, c)
	require.EqualValues(t, 3, c.scheduled.Load(), "every commit above the threshold is scheduled once its DAG is idle")
	require.EqualValues(t, 1, c.compacted.Load(), "only the third turn has history older than the two kept turns")
	require.EqualValues(t, 2, c.skipped.Load())

	root := f.node(t, first)
	child := f.node(t, second)
	compacted := f.node(t, third)
	require.Nil(t, root.Parent)
	require.Equal(t, &root.Ref, child.Parent)
	require.Nil(t, compacted.Parent)
	require.NotEqual(t, root.Ref.DAGID, compacted.Ref.DAGID)
	require.NotNil(t, compacted.RedirectedFrom)
	require.Equal(t, root.Ref.DAGID, compacted.RedirectedFrom.DAGID)
	require.Equal(t, []int{third}, compacted.ReplyMessageIDs)
	mappings := compactFixtureMessages(t, f)
	require.Equal(t, root.Ref, mappings[strconv.Itoa(first)])
	require.Equal(t, child.Ref, mappings[strconv.Itoa(second)])
	require.Equal(t, compacted.Ref, mappings[strconv.Itoa(third)])

	oldThird := *compacted.RedirectedFrom
	turns, err := f.service.Replay(t.Context(), f.scope(), oldThird)
	require.NoError(t, err)
	require.Len(t, turns, 3, "the old DAG keeps its untouched three-node chain")
	rootCapture := f.archive(t, root)
	childCapture := f.archive(t, child)
	archive := f.archive(t, compacted)
	require.NotEmpty(t, archive.Bootstrap)
	summary := archive.Bootstrap[0].Message
	require.Equal(t, schema.User, summary.Role)
	require.True(t, strings.HasPrefix(summary.Content, agentV3SessionSummaryHeader))
	require.Contains(t, summary.Content, compactSummaryText)
	require.Equal(t, sessionRecordMessages(childCapture.Delta), sessionRecordMessages(archive.Bootstrap[1:]), "the kept middle turn replays verbatim after the summary")
	require.Equal(t, turns[2].Delta, sessionRecordMessages(archive.Delta), "the most recent turn becomes the new root's delta")

	summarizerInput := summarizer.capturedInputs()
	require.Len(t, summarizerInput, 1)
	prompt := replySessionSchemaText(summarizerInput[0])
	require.Contains(t, prompt, "FIRST_INPUT")
	require.Contains(t, prompt, "FIRST_FINAL")
	require.NotContains(t, prompt, "THIRD_INPUT", "kept turns are not summarized")
	require.Contains(t, prompt, "Hard limit: 500 characters")
	require.Len(t, rootCapture.Delta, len(turns[0].Delta))

	fourth := sessionMessage(40, 7, 180, "FOURTH_INPUT")
	fourth.ReplyTo = &tb.Message{ID: third}
	fourthID := f.chat(t, cfg, fourth, &config.AgentTrigger{Reply: true})
	require.Equal(t, &compacted.Ref, f.node(t, fourthID).Parent, "replying to the compacted message continues the new root")
	input := replySessionSchemaText(mdl.capturedInputs()[3])
	require.Contains(t, input, compactSummaryText)
	require.Contains(t, input, "SECOND_INPUT")
	require.Contains(t, input, "THIRD_INPUT")
	require.Equal(t, 1, strings.Count(input, "FIRST_INPUT"), "the old first turn survives only inside the summary")
	require.Equal(t, 1, strings.Count(input, "FOURTH_INPUT"))
}

func TestAgentV3SessionCompactionRedirectsOnlyMappingsStillOwnedBySource(t *testing.T) {
	f := newAgentSessionFixture(t)
	cfg := &config.AgentConfig{Name: "race", ContextMode: "reply_chain"}
	f.compile(t, cfg, &scriptedToolModel{turns: [][]*schema.Message{{schema.AssistantMessage("one", nil)}, {schema.AssistantMessage("two", nil)}, {schema.AssistantMessage("three", nil)}}})
	first, second, third := compactChatChain(t, f, cfg, nil)
	source := f.node(t, second)
	grandchild := f.node(t, third)
	require.Equal(t, &source.Ref, grandchild.Parent)
	summarizer := newCompactTestModel()
	summarizer.turns = append(summarizer.turns, summarizer.turns[0])
	c := &agentV3SessionCompactor{service: f.service, cfg: config.AgentV3SessionCompactConfig{Enable: true, KeepRecentTurns: 1, Model: &config.Model{Model: "fake-summarizer"}}, newModel: func(context.Context, *config.Model) (model.ToolCallingChatModel, error) { return summarizer, nil }, ctx: t.Context()}
	job := agentV3SessionCompactJob{scope: f.scope(), node: source, agent: cfg}
	compacted, err := c.compact(t.Context(), job)
	require.NoError(t, err)
	require.Equal(t, []int{second}, compacted.ReplyMessageIDs)
	mappings := compactFixtureMessages(t, f)
	require.Equal(t, f.node(t, first).Ref, mappings[strconv.Itoa(first)])
	require.Equal(t, compacted.Ref, mappings[strconv.Itoa(second)])
	require.Equal(t, grandchild.Ref, mappings[strconv.Itoa(third)], "the child created before the redirect keeps its own mapping")
	require.Equal(t, grandchild, f.node(t, third))
	turns, err := f.service.Replay(t.Context(), f.scope(), grandchild.Ref)
	require.NoError(t, err)
	require.Len(t, turns, 3, "the old DAG is not rewritten")

	_, err = c.compact(t.Context(), job)
	require.ErrorIs(t, err, session.ErrStale, "a second run finds the mapping already moved")
	_, err = c.compact(t.Context(), agentV3SessionCompactJob{scope: f.scope(), node: f.node(t, first), agent: cfg})
	require.ErrorIs(t, err, errAgentV3SessionCompactNothing, "a lone root with empty bootstrap has nothing older to summarize")
	c.cfg.Model, cfg.Format.ProgressSummary = nil, nil
	_, err = c.compact(t.Context(), agentV3SessionCompactJob{scope: f.scope(), node: grandchild, agent: cfg})
	require.ErrorIs(t, err, errAgentV3SessionCompactNoModel)
}

func TestAgentV3SessionCompactSplitAndTranscript(t *testing.T) {
	user := schema.UserMessage
	turns := []session.ReplayTurn{
		{Bootstrap: []*schema.Message{user("boot")}, Delta: []*schema.Message{user("one"), schema.AssistantMessage("a1", nil)}},
		{Delta: []*schema.Message{user("two"), schema.AssistantMessage("a2", nil)}},
		{Delta: []*schema.Message{user("three"), schema.AssistantMessage("a3", nil)}},
	}
	tests := []struct {
		keep       int
		wantOld    []string
		wantRecent int
	}{
		{keep: 1, wantOld: []string{"boot", "one", "a1", "two", "a2"}, wantRecent: 1},
		{keep: 2, wantOld: []string{"boot", "one", "a1"}, wantRecent: 2},
		{keep: 3, wantOld: []string{"boot"}, wantRecent: 3},
		{keep: 9, wantOld: []string{"boot"}, wantRecent: 3},
	}
	for _, tt := range tests {
		old, recent := splitAgentV3SessionTurns(turns, tt.keep)
		var got []string
		for _, message := range old {
			got = append(got, message.Content)
		}
		require.Equal(t, tt.wantOld, got, "keep=%d", tt.keep)
		require.Len(t, recent, tt.wantRecent, "keep=%d", tt.keep)
	}
	old, recent := splitAgentV3SessionTurns(nil, 2)
	require.Nil(t, old)
	require.Nil(t, recent)

	call := &schema.Message{Role: schema.Assistant, ToolCalls: []schema.ToolCall{{ID: "c1", Function: schema.FunctionCall{Name: "lookup", Arguments: `{"q":"x"}`}}}}
	result := &schema.Message{Role: schema.Tool, ToolCallID: "c1", ToolName: "lookup", Content: strings.Repeat("R", agentV3SessionCompactMessageChars+100)}
	image := &schema.Message{Role: schema.User, UserInputMultiContent: []schema.MessageInputPart{{Type: schema.ChatMessagePartTypeImageURL, Image: &schema.MessageInputImage{}}, {Type: schema.ChatMessagePartTypeText, Text: "see https://example.com/a"}}}
	transcript := renderAgentV3SessionTranscript([]*schema.Message{user("hello"), call, result, image, schema.AssistantMessage("", nil)})
	require.Contains(t, transcript, "[user] hello")
	require.Contains(t, transcript, "[assistant] tool_call lookup({\"q\":\"x\"})")
	require.Contains(t, transcript, "[tool lookup] ")
	require.Contains(t, transcript, "characters truncated")
	require.Contains(t, transcript, "[image_url]")
	require.Contains(t, transcript, "https://example.com/a")
	require.False(t, strings.HasSuffix(transcript, "\n"))
	require.Less(t, len([]rune(transcript)), agentV3SessionCompactMessageChars+400)
}

func TestAgentV3SessionCompactSummaryBounds(t *testing.T) {
	long := strings.Repeat("长", 50)
	tests := []struct {
		name    string
		reply   string
		max     int
		want    string
		wantErr error
	}{
		{name: "trimmed", reply: "  summary  ", max: 100, want: "summary"},
		{name: "capped in runes", reply: long, max: 10, want: strings.Repeat("长", 10) + "…"},
		{name: "empty", reply: "   ", max: 100, wantErr: errAgentV3SessionCompactEmptySummary},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mdl := &scriptedToolModel{turns: [][]*schema.Message{{schema.AssistantMessage(tt.reply, nil)}}}
			got, err := summarizeAgentV3SessionHistory(t.Context(), mdl, []*schema.Message{schema.UserMessage("old")}, tt.max)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
			require.Contains(t, mdl.capturedInputs()[0][0].Content, "Hard limit: "+strconv.Itoa(tt.max))
		})
	}
}

func TestAgentV3SessionCompactorQueueDedupesPerDAG(t *testing.T) {
	c := &agentV3SessionCompactor{jobs: make(chan agentV3SessionCompactJob, 1), queued: map[string]struct{}{}}
	job := agentV3SessionCompactJob{node: session.Node{Ref: session.NodeRef{DAGID: "dag-a", NodeID: "n1"}}}
	require.True(t, c.enqueue(job))
	require.False(t, c.enqueue(job), "the same DAG is queued once")
	other := agentV3SessionCompactJob{node: session.Node{Ref: session.NodeRef{DAGID: "dag-b", NodeID: "n2"}}}
	require.False(t, c.enqueue(other), "a full queue drops work instead of blocking the reply")
	require.EqualValues(t, 1, c.scheduled.Load())
	require.EqualValues(t, 1, c.pending.Load())
}
