package agentv3

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"text/template"
	"time"

	"csust-got/agent/session"
	"csust-got/config"
	"csust-got/orm"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
	tb "gopkg.in/telebot.v3"
)

type agentSessionGateRepository struct {
	session.Repository
	resolves     atomic.Int32
	confirms     atomic.Int32
	releases     atomic.Int32
	resolveError error
	confirmError error
	releaseError error
	onConfirm    func()
	released     chan struct{}
}

func (r *agentSessionGateRepository) ResolveAndPin(ctx context.Context, selection session.Selection, token string, duration time.Duration) (session.Pinned, error) {
	r.resolves.Add(1)
	if r.resolveError != nil {
		return session.Pinned{}, r.resolveError
	}
	return r.Repository.ResolveAndPin(ctx, selection, token, duration)
}

func (r *agentSessionGateRepository) ConfirmLoaded(ctx context.Context, scope session.Scope, lease session.Lease, duration time.Duration) error {
	r.confirms.Add(1)
	if r.onConfirm != nil {
		r.onConfirm()
	}
	if r.confirmError != nil {
		return r.confirmError
	}
	return r.Repository.ConfirmLoaded(ctx, scope, lease, duration)
}

func (r *agentSessionGateRepository) Release(ctx context.Context, scope session.Scope, lease session.Lease) error {
	r.releases.Add(1)
	err := errors.Join(r.Repository.Release(ctx, scope, lease), r.releaseError)
	select {
	case r.released <- struct{}{}:
	default:
	}
	return err
}

func installAgentSessionGate(t *testing.T, f *agentSessionFixture, options session.Options) *agentSessionGateRepository {
	t.Helper()
	require.NoError(t, f.service.Close())
	files, err := session.NewFileStore(f.directory)
	require.NoError(t, err)
	repo := &agentSessionGateRepository{Repository: f.repo, released: make(chan struct{}, 8)}
	f.service, err = session.NewService(repo, files, options)
	require.NoError(t, err)
	f.files = files
	done := make(chan struct{})
	close(done)
	agentSessionService.Store(&agentV3SessionService{service: f.service, cancel: func() {}, done: done})
	return repo
}

func seedAgentSession(t *testing.T, f *agentSessionFixture, cfg *config.AgentConfig, id int, messages []*schema.Message, parent *session.LoadedParent) session.Node {
	t.Helper()
	runID, err := session.NewID()
	require.NoError(t, err)
	node, err := f.service.Commit(t.Context(), session.CommitRequest{
		Scope: f.scope(), Agent: cfg.Name, RunID: runID, Parent: parent,
		Capture: session.TurnCapture{Complete: true, Delta: session.History(messages...)},
		Receipt: session.DeliveryReceipt{MessageIDs: []int{id}},
	})
	require.NoError(t, err)
	return node
}

type agentSessionAllRepliesProof struct{}

func (agentSessionAllRepliesProof) ContainsReplyMessageID(int) bool { return true }

func TestAgentV3SessionDefaultOverflowExactBoundary(t *testing.T) {
	for _, tokens := range []int64{200000, 200001} {
		t.Run(strconv.FormatInt(tokens, 10), func(t *testing.T) {
			f := newAgentSessionFixture(t)
			cfg := &config.AgentConfig{Name: "boundary", ContextMode: "reply_chain"}
			mdl := &agentSessionModel{scriptedToolModel: &scriptedToolModel{turns: [][]*schema.Message{{schema.AssistantMessage("FINAL", nil)}}}}
			compiled := f.compile(t, cfg, mdl, lookupTool{})
			current := sessionMessage(1100, 8, 60, "CURRENT")
			current.ReplyTo = sessionMessage(50, 7, 0, "FALLBACK_TARGET")
			tc := &TurnContext{Bot: f.bot, BotUser: f.bot.Me, Config: cfg, Message: current, ChatID: -100, Trigger: &config.AgentTrigger{Reply: true}}
			commonMessages, err := prepareAgentV3Turn(WithTurnContext(t.Context(), tc), compiled, tc, nil)
			require.NoError(t, err)
			replay := []*schema.Message{schema.UserMessage("AAAA"), schema.AssistantMessage("OLD", nil)}
			prepared, err := buildAgentV3LoadedInput(compiled, tc, commonMessages[0].Content, "", replay, agentSessionAllRepliesProof{})
			require.NoError(t, err)
			estimate, err := compiled.Agent.estimateSessionContext(WithTurnContext(t.Context(), tc), prepared.messages, math.MaxInt64)
			require.NoError(t, err)
			replay[0].Content = strings.Repeat("A", int(tokens-estimate.Tokens+1)*4)
			prepared, err = buildAgentV3LoadedInput(compiled, tc, prepared.messages[0].Content, "", replay, agentSessionAllRepliesProof{})
			require.NoError(t, err)
			exact, err := compiled.Agent.estimateSessionContext(WithTurnContext(t.Context(), tc), prepared.messages, math.MaxInt64)
			require.NoError(t, err)
			require.Equal(t, tokens, exact.Tokens, "use real E estimator, not a fake acceptance hook")
			root := seedAgentSession(t, f, cfg, 50, replay, nil)
			before := f.archive(t, root)
			repo := installAgentSessionGate(t, f, session.Options{})
			var parent *session.LoadedParent
			mdl.before = func(ctx context.Context, _ []*schema.Message) error {
				parent = GetTurnContext(ctx).Session.parent
				return nil
			}
			id := f.chat(t, cfg, current, &config.AgentTrigger{Reply: true})
			node := f.node(t, id)
			require.Equal(t, before, f.archive(t, root), "rebuild never rewrites old JSONL")
			require.Equal(t, root, f.node(t, 50), "old node, digest and message index stay immutable")
			if tokens == 200000 {
				require.NotNil(t, parent)
				require.Equal(t, &root.Ref, node.Parent)
				require.EqualValues(t, 1, repo.confirms.Load())
			} else {
				require.Nil(t, parent)
				require.Nil(t, node.Parent)
				require.NotEqual(t, root.Ref.DAGID, node.Ref.DAGID)
				require.Zero(t, repo.confirms.Load())
				require.EqualValues(t, 1, repo.releases.Load())
				require.Contains(t, replySessionSchemaText(sessionRecordMessages(f.archive(t, node).Bootstrap)), "FALLBACK_TARGET")
			}
			require.Len(t, mdl.capturedInputs(), 1, "fallback executes the model exactly once")
			require.Same(t, tc.Message.ReplyTo, current.ReplyTo)
		})
	}
}

func TestAgentV3SessionOverflowOriginalModeAndSaveSwitch(t *testing.T) {
	for _, mode := range []string{"chat", "reply_chain"} {
		for _, save := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/save=%t", mode, save), func(t *testing.T) {
				f := newAgentSessionFixture(t)
				cfg := &config.AgentConfig{Name: "rebuild", ContextMode: mode, Session: config.AgentSessionConfig{SaveContext: &save, LoadContext: true}}
				mdl := &scriptedToolModel{turns: [][]*schema.Message{{schema.AssistantMessage("FINAL", nil)}}}
				f.compile(t, cfg, mdl)
				root := seedAgentSession(t, f, cfg, 50, []*schema.Message{schema.UserMessage(strings.Repeat("OLD_SESSION", 1000)), schema.AssistantMessage("OLD_FINAL", nil)}, nil)
				config.BotConfig.AgentV3.Session.ContextOverflow.MaxTokens = 1
				scope := orm.AgentV3Scope{Bot: f.scope().Bot, Platform: agentV3Platform, ChatID: -100}
				require.NoError(t, orm.AgentV3SetSummary(t.Context(), scope, orm.AgentV3Summary{Content: "ORIGINAL_SUMMARY"}, time.Hour))
				require.NoError(t, orm.AgentV3AppendTurnPair(t.Context(), scope, orm.AgentV3Turn{Role: "user", Content: "ORIGINAL_RAW"}, orm.AgentV3Turn{Role: "assistant", Content: "ORIGINAL_ASSISTANT"}, 12, time.Hour))
				repo := installAgentSessionGate(t, f, session.Options{})
				current := sessionMessage(1100, 8, 60, "CURRENT")
				current.ReplyTo = sessionMessage(50, 7, 0, "DIRECT_TARGET")
				originalReply := current.ReplyTo
				id := f.chat(t, cfg, current, nil)
				require.Same(t, originalReply, current.ReplyTo)
				require.EqualValues(t, 1, repo.resolves.Load())
				require.Zero(t, repo.confirms.Load())
				require.EqualValues(t, 1, repo.releases.Load())
				input := replySessionSchemaText(mdl.capturedInputs()[0])
				if mode == "chat" {
					require.Contains(t, input, "ORIGINAL_SUMMARY")
					require.Contains(t, input, "ORIGINAL_RAW")
				} else {
					require.NotContains(t, input, "ORIGINAL_SUMMARY")
					require.NotContains(t, input, "ORIGINAL_RAW")
					require.Contains(t, input, "DIRECT_TARGET")
				}
				require.Len(t, mdl.capturedInputs(), 1, "fallback over limit does not recurse or replay side effects")
				if save {
					node := f.node(t, id)
					require.Nil(t, node.Parent)
					require.NotEqual(t, root.Ref.DAGID, node.Ref.DAGID)
					capture := f.archive(t, node)
					require.NotEmpty(t, capture.Bootstrap)
				} else {
					_, err := f.service.Load(t.Context(), session.Selection{Scope: f.scope(), Mode: session.SelectReply, ReplyMessageID: id})
					require.ErrorIs(t, err, session.ErrMiss)
				}
			})
		}
	}
}

func TestAgentV3SessionOverflowRejectDoesNotKeepOldDAGAlive(t *testing.T) {
	f := newAgentSessionFixture(t)
	now := time.Date(2026, 10, 8, 10, 0, 0, 0, time.UTC)
	f.mini.SetTime(now)
	save := false
	cfg := &config.AgentConfig{Name: "idle", ContextMode: "reply_chain", Session: config.AgentSessionConfig{SaveContext: &save, LoadContext: true}}
	mdl := &scriptedToolModel{turns: [][]*schema.Message{{schema.AssistantMessage("one", nil)}, {schema.AssistantMessage("two", nil)}}}
	f.compile(t, cfg, mdl)
	root := seedAgentSession(t, f, cfg, 50, []*schema.Message{schema.UserMessage(strings.Repeat("A", 10000)), schema.AssistantMessage("old", nil)}, nil)
	repo := installAgentSessionGate(t, f, session.Options{TTL: time.Hour})
	config.BotConfig.AgentV3.Session.ContextOverflow.MaxTokens = 100
	for index, elapsed := range []time.Duration{30 * time.Minute, 59 * time.Minute} {
		f.mini.SetTime(now.Add(elapsed))
		f.chat(t, cfg, sessionMessage(1100+index*100, 8, 60, "current"), nil)
	}
	require.Zero(t, repo.confirms.Load())
	require.EqualValues(t, 2, repo.releases.Load())
	f.mini.SetTime(now.Add(61 * time.Minute))
	require.NoError(t, f.service.Collect(t.Context()))
	publication, err := f.repo.GetPublication(t.Context(), f.scope(), root.RunID)
	require.NoError(t, err)
	require.Nil(t, publication, "rejected loads neither refresh LastActive nor leave live pins")
}

func TestAgentV3SessionAcceptedLoadOnlyKeepsDAGAlive(t *testing.T) {
	f := newAgentSessionFixture(t)
	now := time.Date(2026, 10, 8, 10, 0, 0, 0, time.UTC)
	f.mini.SetTime(now)
	save := false
	cfg := &config.AgentConfig{Name: "loadonly", ContextMode: "reply_chain", Session: config.AgentSessionConfig{SaveContext: &save, LoadContext: true}}
	f.compile(t, cfg, &scriptedToolModel{turns: [][]*schema.Message{{schema.AssistantMessage("new", nil)}}})
	root := seedAgentSession(t, f, cfg, 50, []*schema.Message{schema.UserMessage("old"), schema.AssistantMessage("answer", nil)}, nil)
	repo := installAgentSessionGate(t, f, session.Options{TTL: time.Hour})
	f.mini.SetTime(now.Add(59 * time.Minute))
	id := f.chat(t, cfg, sessionMessage(1100, 8, 60, "current"), nil)
	require.EqualValues(t, 1, repo.confirms.Load())
	_, err := f.service.Load(t.Context(), session.Selection{Scope: f.scope(), Mode: session.SelectReply, ReplyMessageID: id})
	require.ErrorIs(t, err, session.ErrMiss)
	f.mini.SetTime(now.Add(61 * time.Minute))
	require.NoError(t, f.service.Collect(t.Context()))
	publication, err := f.repo.GetPublication(t.Context(), f.scope(), root.RunID)
	require.NoError(t, err)
	require.NotNil(t, publication, "accepted load-only still refreshes LastActive")
}

func TestAgentV3SessionConfirmFailureDropsCandidateAndPreservesReply(t *testing.T) {
	f := newAgentSessionFixture(t)
	cfg := &config.AgentConfig{Name: "fence", ContextMode: "reply_chain", Session: config.AgentSessionConfig{LoadContext: true}}
	mdl := &scriptedToolModel{turns: [][]*schema.Message{{schema.AssistantMessage("new", nil)}}}
	f.compile(t, cfg, mdl)
	root := seedAgentSession(t, f, cfg, 50, []*schema.Message{schema.UserMessage("CANDIDATE_ONLY"), schema.AssistantMessage("old", nil)}, nil)
	repo := installAgentSessionGate(t, f, session.Options{})
	repo.confirmError = session.ErrFence
	current := sessionMessage(1100, 8, 60, "current")
	current.ReplyTo = sessionMessage(50, 7, 0, "FALLBACK_QUOTE")
	original := current.ReplyTo
	id := f.chat(t, cfg, current, &config.AgentTrigger{Reply: true})
	node := f.node(t, id)
	require.Nil(t, node.Parent)
	require.NotEqual(t, root.Ref.DAGID, node.Ref.DAGID)
	text := replySessionSchemaText(mdl.capturedInputs()[0])
	require.NotContains(t, text, "CANDIDATE_ONLY")
	require.Contains(t, text, "FALLBACK_QUOTE")
	require.Same(t, original, current.ReplyTo)
	require.EqualValues(t, 1, repo.releases.Load())
}

func TestAgentV3SessionMissLogDoesNotHideJoinedFault(t *testing.T) {
	for index, fault := range []error{session.ErrMiss, errors.Join(session.ErrMiss, errAgentSessionFixtureModel)} {
		t.Run(fault.Error(), func(t *testing.T) {
			f := newAgentSessionFixture(t)
			cfg := &config.AgentConfig{Name: "logs", ContextMode: "reply_chain", Session: config.AgentSessionConfig{LoadContext: true}}
			f.compile(t, cfg, &scriptedToolModel{turns: [][]*schema.Message{{schema.AssistantMessage("new", nil)}}})
			repo := installAgentSessionGate(t, f, session.Options{})
			repo.resolveError = fault
			core, logs := observer.New(zap.DebugLevel)
			restore := zap.ReplaceGlobals(zap.New(core))
			defer restore()
			f.chat(t, cfg, sessionMessage(1100, 8, 60, "current"), nil)
			if index == 0 {
				require.Equal(t, 1, logs.FilterLevelExact(zap.DebugLevel).FilterMessage("agentv3: session miss; using legacy context").Len())
			} else {
				require.Equal(t, 1, logs.FilterLevelExact(zap.WarnLevel).FilterMessage("agentv3: session load rejected or failed; using legacy context").Len())
			}
		})
	}
}

func TestAgentV3SessionDisabledOverridesActualReply(t *testing.T) {
	f := newAgentSessionFixture(t)
	enabled := false
	config.BotConfig.AgentV3.Session.Enable = &enabled
	cfg := &config.AgentConfig{Name: "disabled", ContextMode: "reply_chain"}
	mdl := &agentSessionModel{scriptedToolModel: &scriptedToolModel{turns: [][]*schema.Message{{schema.AssistantMessage("new", nil)}}}}
	f.compile(t, cfg, mdl)
	mdl.before = func(ctx context.Context, _ []*schema.Message) error {
		require.Nil(t, GetTurnContext(ctx).Session.capture)
		require.Nil(t, GetTurnContext(ctx).Session.parent)
		return nil
	}
	current := sessionMessage(1100, 8, 60, "current")
	current.ReplyTo = sessionMessage(50, 7, 0, "old")
	f.chat(t, cfg, current, &config.AgentTrigger{Reply: true})
	scopes, err := f.repo.Scopes(t.Context())
	require.NoError(t, err)
	require.Empty(t, scopes)
}

func TestAgentV3SessionBlockedPhotoCallbackCancellation(t *testing.T) {
	for _, stage := range []string{"lookup", "download headers", "download body"} {
		for _, action := range []string{"cancel", "close", "caller deadline beyond operation budget"} {
			t.Run(stage+"/"+action, func(t *testing.T) {
				f := newAgentSessionFixture(t)
				cfg := &config.AgentConfig{Name: "blocked", ContextMode: "reply_chain", Session: config.AgentSessionConfig{LoadContext: true}, Features: config.FeatureSetting{Image: true}}
				mdl := &scriptedToolModel{}
				compiled := f.compile(t, cfg, mdl)
				cfg.Model.Features.Image = true
				root := seedAgentSession(t, f, cfg, 50, []*schema.Message{schema.UserMessage("old"), schema.AssistantMessage("answer", nil)}, nil)
				options := session.Options{}
				if action == "caller deadline beyond operation budget" {
					options.OperationTimeout = 50 * time.Millisecond
				}
				repo := installAgentSessionGate(t, f, options)
				started := make(chan struct{}, 1)
				requestCanceled := make(chan struct{}, 8)
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if strings.HasSuffix(r.URL.Path, "/getFile") && stage != "lookup" {
						_, _ = w.Write([]byte(`{"ok":true,"result":{"file_id":"photo","file_path":"photo.jpg"}}`))
						return
					}
					_, _ = io.Copy(io.Discard, r.Body)
					if stage == "download body" {
						w.WriteHeader(http.StatusOK)
						_, _ = w.Write([]byte("partial image body"))
						w.(http.Flusher).Flush()
					}
					select {
					case started <- struct{}{}:
					default:
					}
					<-r.Context().Done()
					requestCanceled <- struct{}{}
				}))
				defer server.Close()
				originalBotURL := f.bot.URL
				const token = "PRIVATE_TOKEN_CALLBACK_MEDIA"
				mediaBot, err := tb.NewBot(tb.Settings{Token: token, URL: server.URL, Offline: true, Client: server.Client()})
				require.NoError(t, err)
				mediaBot.Me = f.bot.Me
				var logOutput bytes.Buffer
				logger := zap.New(zapcore.NewCore(zapcore.NewJSONEncoder(zap.NewProductionEncoderConfig()), zapcore.AddSync(&logOutput), zap.DebugLevel))
				restoreLogger := zap.ReplaceGlobals(logger)
				defer restoreLogger()
				current := sessionMessage(1100, 8, 60, "current")
				current.Photo = &tb.Photo{File: tb.File{FileID: "photo"}}
				tc := &TurnContext{Bot: mediaBot, BotUser: mediaBot.Me, Message: current, ChatID: -100, Config: cfg}
				ctx, cancel := context.WithCancel(WithTurnContext(t.Context(), tc))
				if action == "caller deadline beyond operation budget" {
					cancel()
					ctx, cancel = context.WithTimeout(WithTurnContext(t.Context(), tc), 300*time.Millisecond)
				}
				defer cancel()
				setupAgentV3SessionTurn(tc)
				returned := make(chan error, 1)
				go func() { _, err := prepareAgentV3Turn(ctx, compiled, tc, nil); returned <- err }()
				select {
				case <-started:
				case <-time.After(3 * time.Second):
					t.Fatal("photo I/O did not reach blocked " + stage)
				}
				require.Same(t, ctx, tc.V3.renderCtx, "callback uses a local rendering context, never overwrites the shared turn")
				switch action {
				case "cancel":
					cancel()
				case "close":
					closed := make(chan error, 1)
					go func() { closed <- f.service.Close() }()
					select {
					case err := <-closed:
						require.NoError(t, err)
					case <-time.After(time.Second):
						t.Fatal("Close waited for blocked HTTP instead of cancelling callback")
					}
					cancel()
				case "caller deadline beyond operation budget":
					select {
					case <-repo.released:
						t.Fatal("short operation budget must not truncate photo rendering")
					case <-time.After(100 * time.Millisecond):
					}
					select {
					case <-repo.released:
					case <-time.After(time.Second):
						t.Fatal("caller deadline did not release its pin")
					}
				}
				select {
				case err := <-returned:
					if action == "caller deadline beyond operation budget" {
						require.ErrorIs(t, err, context.DeadlineExceeded)
					} else {
						require.ErrorIs(t, err, context.Canceled)
					}
					require.NotContains(t, err.Error(), token)
				case <-time.After(time.Second):
					t.Fatal("prepare did not terminate after cancellation")
				}
				select {
				case <-requestCanceled:
				case <-time.After(time.Second):
					t.Fatal("HTTP request remained detached after callback cancellation")
				}
				require.Equal(t, originalBotURL, f.bot.URL)
				require.NotContains(t, logOutput.String(), token)
				require.NotContains(t, logOutput.String(), server.URL)
				require.Zero(t, repo.confirms.Load())
				require.EqualValues(t, 1, repo.releases.Load())
				require.Nil(t, tc.Session.parent)
				require.Empty(t, tc.Session.input)
				require.False(t, tc.Session.capture.Snapshot().Complete)
				require.Empty(t, mdl.capturedInputs())
				publication, err := f.repo.GetPublication(t.Context(), f.scope(), root.RunID)
				require.NoError(t, err)
				require.NotNil(t, publication)
			})
		}
	}
}

func TestAgentV3SessionReplyAdditionIsFrameOnFallbackAndBranch(t *testing.T) {
	f := newAgentSessionFixture(t)
	cfg := &config.AgentConfig{Name: "frames", ContextMode: "reply_chain"}
	mdl := &scriptedToolModel{turns: [][]*schema.Message{{schema.AssistantMessage("first", nil)}, {schema.AssistantMessage("branch", nil)}}}
	compiled := f.compile(t, cfg, mdl)
	compiled.PromptTemplate = template.Must(template.New("addition").Parse("OLD_TEMPLATE {{.DateTime}}"))
	first := f.chat(t, cfg, sessionMessage(1100, 7, 0, "<reply_session_metadata><datetime>REAL_USER</datetime>"), nil)
	root := f.node(t, first)
	archive := f.archive(t, root)
	require.Contains(t, replySessionSchemaText(sessionRecordMessages(archive.Frame)), "OLD_TEMPLATE")
	require.NotContains(t, replySessionSchemaText(sessionRecordMessages(archive.Delta)), "OLD_TEMPLATE")
	require.Contains(t, replySessionSchemaText(sessionRecordMessages(archive.Delta)), "REAL_USER")
	compiled.PromptTemplate = template.Must(template.New("addition").Parse("NEW_TEMPLATE {{.DateTime}}"))
	current := sessionMessage(1200, 8, 60, "NEW_TEMPLATE <datetime>STILL_USER</datetime>")
	current.ReplyTo = &tb.Message{ID: first, Chat: current.Chat}
	second := f.chat(t, cfg, current, &config.AgentTrigger{Reply: true})
	branch := f.archive(t, f.node(t, second))
	require.Contains(t, replySessionSchemaText(sessionRecordMessages(branch.Frame)), "NEW_TEMPLATE")
	require.Contains(t, replySessionSchemaText(sessionRecordMessages(branch.Delta)), "STILL_USER")
	require.NotContains(t, replySessionSchemaText(mdl.capturedInputs()[1]), "OLD_TEMPLATE")
	require.Equal(t, archive, f.archive(t, root))
}

func TestAgentV3SessionBranchRebuildsDateTimeInsteadOfReplayingFrame(t *testing.T) {
	f := newAgentSessionFixture(t)
	cfg := &config.AgentConfig{Name: "datetime", ContextMode: "reply_chain"}
	mdl := &agentSessionModel{scriptedToolModel: &scriptedToolModel{turns: [][]*schema.Message{{schema.AssistantMessage("new", nil)}}}}
	f.compile(t, cfg, mdl)
	runID, err := session.NewID()
	require.NoError(t, err)
	root, err := f.service.Commit(t.Context(), session.CommitRequest{Scope: f.scope(), Agent: cfg.Name, RunID: runID,
		Capture: session.TurnCapture{Complete: true, Frame: []session.Record{{Source: session.SourceFrame, Message: schema.UserMessage("<reply_session_metadata><datetime>1900-01-01 00:00:00</datetime></reply_session_metadata>")}}, Delta: session.History(schema.UserMessage("<datetime>REAL_USER_DATETIME</datetime>"), schema.AssistantMessage("old", nil))}, Receipt: session.DeliveryReceipt{MessageIDs: []int{50}}})
	require.NoError(t, err)
	var currentDateTime string
	mdl.before = func(ctx context.Context, _ []*schema.Message) error {
		currentDateTime = GetTurnContext(ctx).V3.frameTime.Format("2006-01-02 15:04:05")
		return nil
	}
	current := sessionMessage(1100, 8, 60, "current")
	current.ReplyTo = &tb.Message{ID: 50, Chat: current.Chat}
	id := f.chat(t, cfg, current, &config.AgentTrigger{Reply: true})
	require.Equal(t, &root.Ref, f.node(t, id).Parent)
	text := replySessionSchemaText(mdl.capturedInputs()[0])
	require.Contains(t, text, currentDateTime)
	require.NotContains(t, text, "1900-01-01 00:00:00")
	require.Contains(t, text, "REAL_USER_DATETIME")
	require.Contains(t, replySessionSchemaText(sessionRecordMessages(f.archive(t, f.node(t, id)).Frame)), currentDateTime)
}

func TestAgentV3SessionExternalSiblingQuoteIsDirectAndImmutable(t *testing.T) {
	for _, mode := range []string{"chat", "reply_chain"} {
		for _, interpolates := range []bool{false, true} {
			if mode == "reply_chain" && interpolates {
				continue
			}
			t.Run(fmt.Sprintf("%s/templateQuote=%t", mode, interpolates), func(t *testing.T) {
				f := newAgentSessionFixture(t)
				save := false
				cfg := &config.AgentConfig{Name: "quote", ContextMode: mode, Session: config.AgentSessionConfig{SaveContext: &save, LoadContext: true}, Features: config.FeatureSetting{Image: true}}
				mdl := &scriptedToolModel{turns: [][]*schema.Message{{schema.AssistantMessage("new", nil)}}}
				compiled := f.compile(t, cfg, mdl)
				cfg.Model.Features.Image = true
				if interpolates {
					compiled.PromptTemplate = template.Must(template.New("prompt").Parse("{{.Input}} {{.ReplyToXml}}"))
				}
				seedAgentSession(t, f, cfg, 50, []*schema.Message{schema.UserMessage("ROOT"), schema.AssistantMessage("root answer", nil)}, nil)
				loaded, err := f.service.Load(t.Context(), session.Selection{Scope: f.scope(), Mode: session.SelectReply, ReplyMessageID: 50})
				require.NoError(t, err)
				seedAgentSession(t, f, cfg, 60, []*schema.Message{schema.UserMessage("SIBLING_SESSION_DO_NOT_REPLAY"), schema.AssistantMessage("sibling answer", nil)}, loaded.Parent)
				seedAgentSession(t, f, cfg, 70, []*schema.Message{schema.UserMessage("SELECTED_BRANCH"), schema.AssistantMessage("selected answer", nil)}, loaded.Parent)
				require.NoError(t, loaded.Parent.Close())
				oldEncoder := encodeTelegramPhotoDataURL
				var photos []string
				encodeTelegramPhotoDataURL = func(_ *TurnContext, p *tb.Photo) (string, error) {
					photos = append(photos, p.FileID)
					return "data:image/jpeg;base64,aA==", nil
				}
				defer func() { encodeTelegramPhotoDataURL = oldEncoder }()
				current := sessionMessage(1100, 8, 60, "CURRENT")
				target := sessionMessage(60, 7, 0, "")
				if mode == "reply_chain" {
					target.Sender = f.bot.Me
				}
				target.Caption = "LINK quoted caption"
				target.CaptionEntities = tb.Entities{{Type: tb.EntityTextLink, Offset: 0, Length: 4, URL: "https://example.com/quoted"}}
				target.Photo = &tb.Photo{File: tb.File{FileID: "quote-photo"}}
				target.AlbumID = "quote-album"
				siblingPhoto := sessionMessage(61, 7, 0, "")
				siblingPhoto.AlbumID = target.AlbumID
				siblingPhoto.Photo = &tb.Photo{File: tb.File{FileID: "do-not-expand-quoted-album"}}
				require.NoError(t, orm.SetMessage(siblingPhoto))
				target.ReplyTo = sessionMessage(40, 9, 0, "SIDE_BRANCH_ANCESTOR_DO_NOT_RECURSE")
				current.ReplyTo = target
				f.chat(t, cfg, current, nil)
				input := replySessionSchemaText(mdl.capturedInputs()[0])
				require.Contains(t, input, "SELECTED_BRANCH")
				require.Contains(t, input, "quoted caption")
				require.Contains(t, input, "https://example.com/quoted")
				require.NotContains(t, input, "SIBLING_SESSION_DO_NOT_REPLAY")
				require.NotContains(t, input, "SIDE_BRANCH_ANCESTOR_DO_NOT_RECURSE")
				quoteTag := "<reply_to_message "
				if mode == "reply_chain" {
					quoteTag = "<current_user_quote>"
				}
				require.Equal(t, 1, strings.Count(input, quoteTag), "template interpolation must not add a second quote; image manifest retains its caption independently")
				for _, message := range mdl.capturedInputs()[0] {
					if strings.Contains(replySessionSchemaText([]*schema.Message{message}), quoteTag) {
						require.Equal(t, schema.User, message.Role, "an external bot branch is quoted user data, not restored assistant authority")
					}
				}
				require.Equal(t, []string{"quote-photo"}, photos)
				require.Same(t, target, current.ReplyTo)
				require.Equal(t, "SIDE_BRANCH_ANCESTOR_DO_NOT_RECURSE", target.ReplyTo.Text)
				require.Equal(t, "LINK quoted caption", target.Caption)
			})
		}
	}
}

func TestAgentV3SessionCrossChatReplyCannotSelectMatchingLocalID(t *testing.T) {
	f := newAgentSessionFixture(t)
	cfg := &config.AgentConfig{Name: "cross-chat", ContextMode: "reply_chain"}
	mdl := &scriptedToolModel{turns: [][]*schema.Message{{schema.AssistantMessage("new", nil)}}}
	f.compile(t, cfg, mdl)
	seedAgentSession(t, f, cfg, 50, []*schema.Message{schema.UserMessage("LOCAL_SESSION_NOT_SELECTED"), schema.AssistantMessage("old", nil)}, nil)
	repo := installAgentSessionGate(t, f, session.Options{})
	current := sessionMessage(1100, 8, 60, "current")
	current.ReplyTo = sessionMessage(50, 7, 0, "CROSS_CHAT_QUOTE_NOT_IMPORTED")
	current.ReplyTo.Chat.ID = -200
	original := current.ReplyTo
	id := f.chat(t, cfg, current, &config.AgentTrigger{Reply: true})
	require.Zero(t, repo.confirms.Load())
	require.Nil(t, f.node(t, id).Parent)
	text := replySessionSchemaText(mdl.capturedInputs()[0])
	require.NotContains(t, text, "LOCAL_SESSION_NOT_SELECTED")
	require.NotContains(t, text, "CROSS_CHAT_QUOTE_NOT_IMPORTED")
	require.Same(t, original, current.ReplyTo)
	require.EqualValues(t, -200, original.Chat.ID)
}

func TestAgentV3SessionUnknownEstimateRejectsCandidate(t *testing.T) {
	f := newAgentSessionFixture(t)
	cfg := &config.AgentConfig{Name: "unknown", ContextMode: "reply_chain", Session: config.AgentSessionConfig{LoadContext: true}}
	mdl := &scriptedToolModel{turns: [][]*schema.Message{{schema.AssistantMessage("new", nil)}}}
	f.compile(t, cfg, mdl)
	root := seedAgentSession(t, f, cfg, 50, []*schema.Message{{Role: schema.User, UserInputMultiContent: []schema.MessageInputPart{{Type: "unknown-part"}}}, schema.AssistantMessage("old", nil)}, nil)
	repo := installAgentSessionGate(t, f, session.Options{})
	core, logs := observer.New(zap.WarnLevel)
	restore := zap.ReplaceGlobals(zap.New(core))
	defer restore()
	id := f.chat(t, cfg, sessionMessage(1100, 8, 60, "current"), nil)
	require.Nil(t, f.node(t, id).Parent)
	require.Zero(t, repo.confirms.Load())
	require.EqualValues(t, 1, repo.releases.Load())
	require.Equal(t, 1, logs.FilterMessage("agentv3: session load rejected or failed; using legacy context").Len())
	require.Equal(t, root, f.node(t, 50))
}

type agentSessionDiagnosticTool struct{ t *testing.T }

func (diagnostic agentSessionDiagnosticTool) Info(context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{Name: "diagnostic", Extra: map[string]any{"unsupported_metadata": func() { diagnostic.t.Fatal("estimation must not execute tool metadata") }}}, nil
}

func (diagnostic agentSessionDiagnosticTool) InvokableRun(context.Context, string, ...tool.Option) (string, error) {
	diagnostic.t.Fatal("candidate acceptance must not run a tool")
	return "", errAgentSessionFixtureModel
}

func TestAgentV3SessionCachedToolEstimateErrorFallsBackWithoutBlockingOtherAgents(t *testing.T) {
	f := newAgentSessionFixture(t)
	cfg := &config.AgentConfig{Name: "cached-estimate-error", ContextMode: "reply_chain", Session: config.AgentSessionConfig{LoadContext: true}}
	mdl := &scriptedToolModel{turns: [][]*schema.Message{{schema.AssistantMessage("fallback still works", nil)}}}
	compiled := f.compile(t, cfg, mdl, agentSessionDiagnosticTool{t: t})
	_, err := compiled.Agent.estimateSessionContext(t.Context(), []*schema.Message{schema.UserMessage("input")}, 200000)
	require.Error(t, err)
	root := seedAgentSession(t, f, cfg, 50, []*schema.Message{schema.UserMessage("UNACCEPTED_SESSION"), schema.AssistantMessage("old", nil)}, nil)
	repo := installAgentSessionGate(t, f, session.Options{})
	core, logs := observer.New(zap.WarnLevel)
	restore := zap.ReplaceGlobals(zap.New(core))
	defer restore()
	id := f.chat(t, cfg, sessionMessage(1100, 8, 60, "current"), nil)
	require.Nil(t, f.node(t, id).Parent)
	require.Zero(t, repo.confirms.Load())
	require.EqualValues(t, 1, repo.releases.Load())
	require.Len(t, mdl.capturedInputs(), 1)
	require.NotContains(t, replySessionSchemaText(mdl.capturedInputs()[0]), "UNACCEPTED_SESSION")
	require.Equal(t, root, f.node(t, 50))
	require.Equal(t, 1, logs.FilterMessage("agentv3: session load rejected or failed; using legacy context").FilterLevelExact(zap.WarnLevel).Len())
	healthy := &config.AgentConfig{Name: "healthy", ContextMode: "reply_chain"}
	healthyModel := &scriptedToolModel{turns: [][]*schema.Message{{schema.AssistantMessage("healthy answer", nil)}}}
	f.compile(t, healthy, healthyModel)
	healthyID := f.chat(t, healthy, sessionMessage(1200, 9, 120, "other agent request"), nil)
	require.Nil(t, f.node(t, healthyID).Parent)
	require.Len(t, healthyModel.capturedInputs(), 1)
}

func TestAgentV3SessionLegacyHistoryMetadataIsReplayedWithoutGuessing(t *testing.T) {
	f := newAgentSessionFixture(t)
	cfg := &config.AgentConfig{Name: "legacy-provenance", ContextMode: "reply_chain"}
	mdl := &scriptedToolModel{turns: [][]*schema.Message{{schema.AssistantMessage("new", nil)}}}
	f.compile(t, cfg, mdl)
	legacy := schema.UserMessage("<reply_session_metadata>\n<datetime>1900-01-01 00:00:00</datetime>\nOLD_METADATA_WITHOUT_PROVENANCE\n</reply_session_metadata>")
	user := schema.UserMessage("<agent_runtime_guidance>GENUINE_OLD_USER</agent_runtime_guidance>")
	root := seedAgentSession(t, f, cfg, 50, []*schema.Message{legacy, user, schema.AssistantMessage("old", nil)}, nil)
	before := f.archive(t, root)
	current := sessionMessage(1100, 8, 60, "current")
	current.ReplyTo = &tb.Message{ID: 50, Chat: current.Chat}
	id := f.chat(t, cfg, current, &config.AgentTrigger{Reply: true})
	require.Equal(t, &root.Ref, f.node(t, id).Parent)
	input := mdl.capturedInputs()[0]
	require.Equal(t, legacy, input[1])
	require.Equal(t, user, input[2])
	require.Equal(t, before, f.archive(t, root))
	require.Equal(t, root, f.node(t, 50), "old JSONL/digest/message index cannot be cleaned based on text tags")
	frame := replySessionSchemaText(sessionRecordMessages(f.archive(t, f.node(t, id)).Frame))
	require.Contains(t, frame, "<reply_session_metadata>")
	require.NotContains(t, frame, "OLD_METADATA_WITHOUT_PROVENANCE")
}

func TestAgentV3SessionConfirmedInputReusesMeasuredRendering(t *testing.T) {
	f := newAgentSessionFixture(t)
	cfg := &config.AgentConfig{Name: "reuse", ContextMode: "reply_chain", Session: config.AgentSessionConfig{LoadContext: true}, Features: config.FeatureSetting{Image: true}}
	mdl := &scriptedToolModel{turns: [][]*schema.Message{{schema.AssistantMessage("new", nil)}}}
	compiled := f.compile(t, cfg, mdl, lookupTool{})
	cfg.Model.Features.Image = true
	compiled.PromptTemplate = template.Must(template.New("addition").Parse("MEASURED_TEMPLATE {{.DateTime}}"))
	scope := orm.AgentV3Scope{Bot: f.scope().Bot, Platform: agentV3Platform, ChatID: -100}
	config.BotConfig.AgentV3.Memory.Enable = true
	config.BotConfig.AgentV3.ContextCache.Enable = true
	require.NoError(t, addAgentV3Memory(t.Context(), scope, 7, "MEASURED_MEMORY"))
	seedAgentSession(t, f, cfg, 50, []*schema.Message{schema.UserMessage("old"), schema.AssistantMessage("answer", nil)}, nil)
	repo := installAgentSessionGate(t, f, session.Options{})
	repo.onConfirm = func() {
		compiled.PromptTemplate = template.Must(template.New("addition").Parse("DO_NOT_RENDER_AFTER_CONFIRM"))
		require.NoError(t, addAgentV3Memory(t.Context(), scope, 7, "DO_NOT_READ_AFTER_CONFIRM"))
	}
	oldEncoder := encodeTelegramPhotoDataURL
	var encodes int
	encodeTelegramPhotoDataURL = func(*TurnContext, *tb.Photo) (string, error) {
		encodes++
		return fmt.Sprintf("data:image/jpeg;base64,ENCODE%d", encodes), nil
	}
	defer func() { encodeTelegramPhotoDataURL = oldEncoder }()
	current := sessionMessage(1100, 8, 60, "current")
	current.Photo = &tb.Photo{File: tb.File{FileID: "photo"}}
	id := f.chat(t, cfg, current, nil)
	require.Equal(t, 1, encodes)
	input := mdl.capturedInputs()[0]
	text := replySessionSchemaText(input)
	require.Contains(t, text, "MEASURED_TEMPLATE")
	require.Contains(t, text, "MEASURED_MEMORY")
	require.NotContains(t, text, "DO_NOT_RENDER_AFTER_CONFIRM")
	require.NotContains(t, text, "DO_NOT_READ_AFTER_CONFIRM")
	require.Equal(t, "data:image/jpeg;base64,ENCODE1", *input[len(input)-1].UserInputMultiContent[1].Image.URL)
	capture := f.archive(t, f.node(t, id))
	require.Contains(t, replySessionSchemaText(sessionRecordMessages(capture.Frame)), "MEASURED_TEMPLATE")
	require.Contains(t, replySessionSchemaText(sessionRecordMessages(capture.Frame)), "MEASURED_MEMORY")
	require.EqualValues(t, 1, repo.confirms.Load())
}

func TestAgentV3SessionCurrentFrameAndToolsCanCauseOverflow(t *testing.T) {
	for _, addition := range []string{"system", "memory", "tools"} {
		t.Run(addition, func(t *testing.T) {
			f := newAgentSessionFixture(t)
			cfg := &config.AgentConfig{Name: "frame-budget", ContextMode: "reply_chain", Session: config.AgentSessionConfig{LoadContext: true}}
			mdl := &scriptedToolModel{turns: [][]*schema.Message{{schema.AssistantMessage("new", nil)}}}
			compiled := f.compile(t, cfg, mdl, lookupTool{})
			seedAgentSession(t, f, cfg, 50, []*schema.Message{schema.UserMessage("old"), schema.AssistantMessage("answer", nil)}, nil)
			current := sessionMessage(1100, 8, 60, "current")
			tc := &TurnContext{Bot: f.bot, BotUser: f.bot.Me, Message: current, ChatID: -100, Config: cfg}
			common, err := prepareAgentV3Turn(WithTurnContext(t.Context(), tc), compiled, tc, nil)
			require.NoError(t, err)
			prepared, err := buildAgentV3LoadedInput(compiled, tc, common[0].Content, "", []*schema.Message{schema.UserMessage("old"), schema.AssistantMessage("answer", nil)}, nil)
			require.NoError(t, err)
			plain, err := NewCustomAgent(t.Context(), &CustomAgentConfig{Name: cfg.Name, Model: &scriptedToolModel{}, MaxSteps: 4})
			require.NoError(t, err)
			baseAgent := compiled.Agent
			if addition == "tools" {
				baseAgent = plain
			}
			base, err := baseAgent.estimateSessionContext(WithTurnContext(t.Context(), tc), prepared.messages, math.MaxInt64)
			require.NoError(t, err)
			config.BotConfig.AgentV3.Session.ContextOverflow.MaxTokens = base.Tokens
			switch addition {
			case "system":
				compiled.SystemTemplate = template.Must(template.New("system").Parse(strings.Repeat("SYSTEM", 1000)))
			case "memory":
				config.BotConfig.AgentV3.Memory.Enable = true
				require.NoError(t, addAgentV3Memory(t.Context(), tc.V3.Scope, 7, strings.Repeat("MEMORY", 1000)))
			case "tools":
				withTools, err := compiled.Agent.estimateSessionContext(WithTurnContext(t.Context(), tc), prepared.messages, math.MaxInt64)
				require.NoError(t, err)
				require.Greater(t, withTools.Tokens, base.Tokens, "actual bound tool schema is counted")
			}
			repo := installAgentSessionGate(t, f, session.Options{})
			id := f.chat(t, cfg, current, nil)
			require.Zero(t, repo.confirms.Load())
			require.Nil(t, f.node(t, id).Parent)
			require.Len(t, mdl.capturedInputs(), 1)
		})
	}
}

func TestAgentV3SessionOverflowCleanupFaultIsWarn(t *testing.T) {
	f := newAgentSessionFixture(t)
	cfg := &config.AgentConfig{Name: "cleanup", ContextMode: "reply_chain", Session: config.AgentSessionConfig{LoadContext: true}}
	f.compile(t, cfg, &scriptedToolModel{turns: [][]*schema.Message{{schema.AssistantMessage("new", nil)}}})
	seedAgentSession(t, f, cfg, 50, []*schema.Message{schema.UserMessage("old"), schema.AssistantMessage("answer", nil)}, nil)
	repo := installAgentSessionGate(t, f, session.Options{})
	repo.releaseError = errAgentSessionFixtureModel
	config.BotConfig.AgentV3.Session.ContextOverflow.MaxTokens = 1
	core, logs := observer.New(zap.DebugLevel)
	restore := zap.ReplaceGlobals(zap.New(core))
	defer restore()
	f.chat(t, cfg, sessionMessage(1100, 8, 60, "current"), nil)
	require.Zero(t, repo.confirms.Load())
	require.Equal(t, 1, logs.FilterMessage("agentv3: session load rejected or failed; using legacy context").FilterLevelExact(zap.WarnLevel).Len())
}

func TestAgentV3SessionCancellationDuringConfirmationHasNoBaseline(t *testing.T) {
	f := newAgentSessionFixture(t)
	cfg := &config.AgentConfig{Name: "cancel-confirm", ContextMode: "reply_chain", Session: config.AgentSessionConfig{LoadContext: true}}
	mdl := &scriptedToolModel{}
	compiled := f.compile(t, cfg, mdl)
	seedAgentSession(t, f, cfg, 50, []*schema.Message{schema.UserMessage("old"), schema.AssistantMessage("answer", nil)}, nil)
	repo := installAgentSessionGate(t, f, session.Options{})
	tc := &TurnContext{Bot: f.bot, BotUser: f.bot.Me, Message: sessionMessage(1100, 8, 60, "current"), ChatID: -100, Config: cfg}
	ctx, cancel := context.WithCancel(WithTurnContext(t.Context(), tc))
	defer cancel()
	repo.onConfirm = cancel
	setupAgentV3SessionTurn(tc)
	_, err := prepareAgentV3Turn(ctx, compiled, tc, nil)
	require.ErrorIs(t, err, context.Canceled)
	require.EqualValues(t, 1, repo.releases.Load())
	require.Nil(t, tc.Session.parent)
	require.Empty(t, tc.Session.input)
	require.Empty(t, tc.Session.kinds)
	require.Empty(t, tc.V3.ImageRefs)
	require.Empty(t, mdl.capturedInputs())
}
