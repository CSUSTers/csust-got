package agentv3

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"csust-got/agent/session"
	"csust-got/config"

	einoopenai "github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/schema"
	openai "github.com/meguminnnnnnnnn/go-openai"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
	tb "gopkg.in/telebot.v3"
)

func TestAgentV3SessionProviderContextRejectsParentOnNextInvocation(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		for _, save := range []bool{false, true} {
			t.Run(fmt.Sprintf("stream=%t/save=%t", streaming, save), func(t *testing.T) {
				f := newAgentSessionFixture(t)
				cfg := &config.AgentConfig{Name: "provider-reject", ContextMode: "reply_chain", Session: config.AgentSessionConfig{SaveContext: &save, LoadContext: true}, Format: config.AgentOutputConfig{StreamOutput: streaming}}
				var attempts atomic.Int32
				var failedTrace *AgentV3Trace
				mdl := &agentSessionModel{scriptedToolModel: &scriptedToolModel{turns: [][]*schema.Message{{schema.AssistantMessage("fallback final", nil)}}}}
				mdl.before = func(ctx context.Context, input []*schema.Message) error {
					if attempts.Add(1) == 1 {
						failedTrace = GetTurnContext(ctx).V3.Trace
						require.NotNil(t, GetTurnContext(ctx).Session.parent)
						return fmt.Errorf("model: %w", &openai.APIError{Code: "context_length_exceeded", HTTPStatusCode: 400, Message: "PRIVATE_PROVIDER_TOKEN <html>private error</html>"})
					}
					require.Nil(t, GetTurnContext(ctx).Session.parent)
					require.NotContains(t, replySessionSchemaText(input), "REJECTED_ARCHIVE")
					return nil
				}
				f.compile(t, cfg, mdl)
				root := seedAgentSession(t, f, cfg, 50, []*schema.Message{schema.UserMessage("REJECTED_ARCHIVE"), schema.AssistantMessage("old", nil)}, nil)
				before := f.archive(t, root)
				repo := installAgentSessionGate(t, f, session.Options{})
				core, logs := observer.New(zap.DebugLevel)
				restore := zap.ReplaceGlobals(zap.New(core))
				defer restore()
				f.chat(t, cfg, sessionMessage(1100, 7, 0, "first input"), nil)
				traceJSON, err := json.Marshal(failedTrace)
				require.NoError(t, err)
				require.Contains(t, string(traceJSON), errAgentV3ProviderContextLimit.Error())
				require.Contains(t, string(traceJSON), "private error", "the trace keeps the truncated provider body for diagnosis")
				require.EqualValues(t, 1, attempts.Load(), "context rejection does not retry this invocation")
				require.EqualValues(t, 1, repo.confirms.Load())
				f.chat(t, cfg, sessionMessage(1200, 7, 60, "second input"), nil)
				require.EqualValues(t, 2, attempts.Load())
				require.EqualValues(t, 1, repo.confirms.Load(), "known-rejected node is not confirmed again")
				require.EqualValues(t, 1, repo.releases.Load(), "known-rejected selection does not acquire a second pin")
				require.Equal(t, before, f.archive(t, root))
				require.Equal(t, root, f.node(t, 50))
				for _, entry := range logs.All() {
					require.NotContains(t, entry.Message, "PRIVATE_PROVIDER_TOKEN")
					require.NotContains(t, fmt.Sprint(entry.ContextMap()), "PRIVATE_PROVIDER_TOKEN")
					require.NotContains(t, fmt.Sprint(entry.ContextMap()), "<html>")
				}
			})
		}
	}
}

func TestAgentV3SessionKnownProviderRejectionDoesNotRefreshLoadOnlyActivity(t *testing.T) {
	f := newAgentSessionFixture(t)
	now := time.Date(2026, 10, 8, 10, 0, 0, 0, time.UTC)
	f.mini.SetTime(now)
	save := false
	cfg := &config.AgentConfig{Name: "rejected-idle", ContextMode: "reply_chain", Session: config.AgentSessionConfig{SaveContext: &save, LoadContext: true}}
	var attempts atomic.Int32
	mdl := &agentSessionModel{scriptedToolModel: &scriptedToolModel{turns: [][]*schema.Message{{schema.AssistantMessage("fallback one", nil)}, {schema.AssistantMessage("fallback two", nil)}}}, before: func(context.Context, []*schema.Message) error {
		if attempts.Add(1) == 1 {
			return &einoopenai.APIError{Code: "context_length_exceeded", HTTPStatusCode: 400}
		}
		return nil
	}}
	f.compile(t, cfg, mdl)
	root := seedAgentSession(t, f, cfg, 50, []*schema.Message{schema.UserMessage("old"), schema.AssistantMessage("answer", nil)}, nil)
	repo := installAgentSessionGate(t, f, session.Options{TTL: time.Hour})
	for index, elapsed := range []time.Duration{30 * time.Minute, 59 * time.Minute, 89 * time.Minute} {
		f.mini.SetTime(now.Add(elapsed))
		f.chat(t, cfg, sessionMessage(1100+index*100, 7, 0, "input"), nil)
	}
	require.EqualValues(t, 1, repo.confirms.Load(), "only the first, not-yet-known rejection can confirm")
	f.mini.SetTime(now.Add(91 * time.Minute))
	require.NoError(t, f.service.Collect(t.Context()))
	publication, err := f.repo.GetPublication(t.Context(), f.scope(), root.RunID)
	require.NoError(t, err)
	require.Nil(t, publication)
}

func TestAgentV3SessionActualSDKReaderFailureKeepsSafeErrorIdentity(t *testing.T) {
	f := newAgentSessionFixture(t)
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		if requests.Add(1) == 1 {
			_, _ = w.Write([]byte("data: {\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"partial answer\"}}]}\n\n"))
			w.(http.Flusher).Flush()
			_, _ = w.Write([]byte("data: {\"error\":{\"code\":\"context_length_exceeded\",\"message\":\"PRIVATE_SDK_TOKEN <html>private</html>\"}}\n\n"))
			return
		}
		_, _ = w.Write([]byte("data: {\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"fallback answer\"}}]}\n\ndata: [DONE]\n\n"))
	}))
	defer server.Close()
	modelConfig := &config.Model{Model: "fixture", BaseUrl: server.URL, ApiKey: "PRIVATE_SDK_TOKEN"}
	mdl, err := einoopenai.NewChatModel(t.Context(), &einoopenai.ChatModelConfig{Model: modelConfig.Model, BaseURL: modelConfig.BaseUrl, APIKey: modelConfig.ApiKey})
	require.NoError(t, err)
	save := false
	cfg := &config.AgentConfig{Name: "sdk-reader", ContextMode: "reply_chain", Session: config.AgentSessionConfig{SaveContext: &save, LoadContext: true}, Format: config.AgentOutputConfig{StreamOutput: true, EditInterval: "1h"}}
	f.compile(t, cfg, mdl)
	cfg.Model = modelConfig
	seedAgentSession(t, f, cfg, 50, []*schema.Message{schema.UserMessage("old"), schema.AssistantMessage("answer", nil)}, nil)
	repo := installAgentSessionGate(t, f, session.Options{})
	oldStream := streamAgentV3
	var turn *TurnContext
	streamAgentV3 = func(_ *CustomAgent, ctx context.Context, input []*schema.Message) (*schema.StreamReader[*schema.Message], error) {
		turn = GetTurnContext(ctx)
		return mdl.Stream(ctx, input)
	}
	t.Cleanup(func() { streamAgentV3 = oldStream })
	core, logs := observer.New(zap.DebugLevel)
	restore := zap.ReplaceGlobals(zap.New(core))
	defer restore()
	err = Chat(f.bot.NewContext(tb.Update{Message: sessionMessage(1100, 7, 0, "first")}), cfg, nil)
	require.ErrorIs(t, err, errAgentV3ProviderContextLimit)
	var apiErr *openai.APIError
	require.ErrorAs(t, err, &apiErr)
	require.Equal(t, "context_length_exceeded", apiErr.Code)
	require.NotContains(t, err.Error(), "PRIVATE_SDK_TOKEN")
	require.NotContains(t, err.Error(), "<html>")
	traceJSON, err := json.Marshal(turn.V3.Trace)
	require.NoError(t, err)
	require.Contains(t, string(traceJSON), errAgentV3ProviderContextLimit.Error())
	require.EqualValues(t, 1, requests.Load(), "partial reader failure does not replay the operation")
	f.chat(t, cfg, sessionMessage(1200, 7, 60, "next"), nil)
	require.EqualValues(t, 2, requests.Load())
	require.EqualValues(t, 1, repo.confirms.Load())
	for _, entry := range logs.All() {
		require.NotContains(t, fmt.Sprint(entry.ContextMap()), "PRIVATE_SDK_TOKEN")
	}
}

func TestAgentV3SessionUnknownProviderErrorDoesNotRejectParent(t *testing.T) {
	for _, fault := range []error{
		errAgentV3ContextToolText,
		&openai.APIError{HTTPStatusCode: 429, Code: "rate_limit_exceeded", Message: "context_length_exceeded"},
		&openai.RequestError{HTTPStatusCode: 400, Body: []byte("<html>bad request</html>"), Err: errAgentV3UnknownProvider},
		&openai.RequestError{HTTPStatusCode: 502, Body: []byte("This model's maximum context length is 262144 tokens"), Err: errAgentV3UnknownProvider},
	} {
		t.Run(fmt.Sprintf("%T", fault), func(t *testing.T) {
			f := newAgentSessionFixture(t)
			save := false
			cfg := &config.AgentConfig{Name: "not-rejected", ContextMode: "reply_chain", Session: config.AgentSessionConfig{SaveContext: &save, LoadContext: true}}
			var attempts atomic.Int32
			mdl := &agentSessionModel{scriptedToolModel: &scriptedToolModel{turns: [][]*schema.Message{{schema.AssistantMessage("success", nil)}}}, before: func(ctx context.Context, _ []*schema.Message) error {
				require.NotNil(t, GetTurnContext(ctx).Session.parent)
				if attempts.Add(1) == 1 {
					return fault
				}
				return nil
			}}
			f.compile(t, cfg, mdl)
			seedAgentSession(t, f, cfg, 50, []*schema.Message{schema.UserMessage("old"), schema.AssistantMessage("answer", nil)}, nil)
			repo := installAgentSessionGate(t, f, session.Options{})
			f.chat(t, cfg, sessionMessage(1100, 7, 0, "first"), nil)
			f.chat(t, cfg, sessionMessage(1200, 7, 60, "second"), nil)
			require.EqualValues(t, 2, repo.confirms.Load())
		})
	}
}

// A context-limit failure after a tool round stems from this turn's own growth, so the
// loaded parent stays usable; the failed turn itself is never committed or replayed.
func TestAgentV3SessionProviderContextFailureAfterToolRoundKeepsParent(t *testing.T) {
	f := newAgentSessionFixture(t)
	cfg := &config.AgentConfig{Name: "tool-once", ContextMode: "reply_chain", Session: config.AgentSessionConfig{LoadContext: true}}
	var attempts atomic.Int32
	mdl := &agentSessionModel{scriptedToolModel: &scriptedToolModel{turns: [][]*schema.Message{{sessionToolMessage("side-effect")}, {schema.AssistantMessage("next fallback", nil)}}}, before: func(context.Context, []*schema.Message) error {
		if attempts.Add(1) == 2 {
			return &openai.APIError{Code: "context_length_exceeded", HTTPStatusCode: 400}
		}
		return nil
	}}
	tool := &countingLookupTool{}
	f.compile(t, cfg, mdl, tool)
	seedAgentSession(t, f, cfg, 50, []*schema.Message{schema.UserMessage("old"), schema.AssistantMessage("answer", nil)}, nil)
	repo := installAgentSessionGate(t, f, session.Options{})
	f.chat(t, cfg, sessionMessage(1100, 7, 0, "first"), nil)
	require.EqualValues(t, 2, attempts.Load())
	tool.mu.Lock()
	require.Equal(t, 1, tool.calls)
	tool.mu.Unlock()
	f.chat(t, cfg, sessionMessage(1200, 7, 60, "second"), nil)
	require.EqualValues(t, 3, attempts.Load())
	require.EqualValues(t, 2, repo.confirms.Load(), "a failure after the first model call does not reject the parent")
	tool.mu.Lock()
	require.Equal(t, 1, tool.calls, "the failed turn's tool side effects are not replayed")
	tool.mu.Unlock()
	require.Len(t, mdl.capturedInputs(), 2, "the failing attempt never reaches the scripted model")
	require.NotContains(t, replySessionSchemaText(mdl.capturedInputs()[1]), "side-effect", "the uncommitted failed turn is not part of the replayed history")
}

func TestAgentV3SessionContextRejectionDoesNotPoisonOtherAgentOrModel(t *testing.T) {
	f := newAgentSessionFixture(t)
	save := false
	cfg := &config.AgentConfig{Name: "original-context", ContextMode: "reply_chain", Session: config.AgentSessionConfig{SaveContext: &save, LoadContext: true}}
	var attempts atomic.Int32
	mdl := &agentSessionModel{scriptedToolModel: &scriptedToolModel{turns: [][]*schema.Message{{schema.AssistantMessage("new model works", nil)}, {schema.AssistantMessage("fallback works", nil)}}}, before: func(ctx context.Context, _ []*schema.Message) error {
		switch attempts.Add(1) {
		case 1:
			require.NotNil(t, GetTurnContext(ctx).Session.parent)
			return &openai.APIError{Code: "context_length_exceeded", HTTPStatusCode: 400}
		case 2:
			require.NotNil(t, GetTurnContext(ctx).Session.parent, "different model can accept the same node")
		default:
			require.Nil(t, GetTurnContext(ctx).Session.parent, "original identity remains rejected")
		}
		return nil
	}}
	f.compile(t, cfg, mdl)
	root := seedAgentSession(t, f, cfg, 50, []*schema.Message{schema.UserMessage("old"), schema.AssistantMessage("answer", nil)}, nil)
	repo := installAgentSessionGate(t, f, session.Options{})
	f.chat(t, cfg, sessionMessage(1100, 7, 0, "first"), nil)
	other := &config.AgentConfig{Name: "other-context", ContextMode: "reply_chain"}
	otherModel := &agentSessionModel{scriptedToolModel: &scriptedToolModel{turns: [][]*schema.Message{{schema.AssistantMessage("other works", nil)}}}, before: func(ctx context.Context, _ []*schema.Message) error {
		require.NotNil(t, GetTurnContext(ctx).Session.parent, "different agent can accept the same node")
		return nil
	}}
	f.compile(t, other, otherModel)
	current := sessionMessage(1200, 7, 60, "other")
	current.ReplyTo = sessionMessage(50, 8, 0, "quote")
	f.chat(t, other, current, &config.AgentTrigger{Reply: true})
	cfg.Model.Model = "other-model"
	f.chat(t, cfg, sessionMessage(1300, 7, 120, "changed model"), nil)
	cfg.Model.Model = "fixture"
	f.chat(t, cfg, sessionMessage(1400, 7, 180, "original model"), nil)
	require.EqualValues(t, 3, repo.confirms.Load())
	require.Equal(t, root, f.node(t, 50))
}
