package agentv3

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"text/template"
	"time"

	"csust-got/agent/session"
	"csust-got/config"

	"github.com/cloudwego/eino/schema"
	"github.com/stretchr/testify/require"
	tb "gopkg.in/telebot.v3"
)

var (
	errAgentSessionFixtureRecovery   = errors.New("recoverable recovery failure")
	errAgentSessionFixtureCollection = errors.New("recoverable collection failure")
)

func TestAgentV3SessionInitRecoversWithoutCollectingExpiredActiveDAG(t *testing.T) {
	f := newAgentSessionFixture(t)
	now := time.Date(2026, 10, 7, 10, 0, 0, 0, time.UTC)
	f.mini.SetTime(now)
	cfg := &config.AgentConfig{Name: "old", ContextMode: "reply_chain"}
	f.compile(t, cfg, &scriptedToolModel{turns: [][]*schema.Message{{schema.AssistantMessage("deleting answer", nil)}, {schema.AssistantMessage("active answer", nil)}}})
	f.chat(t, cfg, sessionMessage(100, 7, 0, "deleting input"), nil)
	f.mini.SetTime(now.Add(59 * time.Minute))
	id := f.chat(t, cfg, sessionMessage(200, 7, 60, "active input"), nil)
	node := f.node(t, id)
	f.mini.SetTime(now.Add(61 * time.Minute))
	deletions, err := f.repo.ClaimDeleting(t.Context(), f.scope(), time.Hour)
	require.NoError(t, err)
	require.Len(t, deletions, 1)
	f.mini.SetTime(now.Add(2 * time.Hour))
	config.BotConfig.AgentV3.Session.TTL = "1h"
	config.BotConfig.Agents = &config.AgentV3Configs{cfg}
	oldManager, oldCron := mcpManager, cronService.Load()
	mcpManager = nil
	cronService.Store(nil)
	t.Cleanup(func() { Close(); mcpManager = oldManager; cronService.Store(oldCron) })
	require.NoError(t, Init(t.Context()))
	s := agentSessionService.Load()
	require.NotNil(t, s)
	require.Eventually(t, func() bool {
		deleting, err := f.repo.Deleting(t.Context(), f.scope())
		return err == nil && len(deleting) == 0
	}, 3*time.Second, 10*time.Millisecond)
	published, err := f.repo.GetPublication(t.Context(), f.scope(), node.RunID)
	require.NoError(t, err)
	require.NotNil(t, published, "startup recovery must leave ordinary expired active DAGs intact")
	require.Nil(t, cronService.Load())
}

type agentSessionCollectionRepository struct {
	session.Repository
	scopes func(context.Context) ([]session.Scope, error)
}

func (r *agentSessionCollectionRepository) Scopes(ctx context.Context) ([]session.Scope, error) {
	return r.scopes(ctx)
}

func TestAgentV3SessionCloseCancelsMaintenanceAndConcurrentCloseWaits(t *testing.T) {
	f := newAgentSessionFixture(t)
	isolateAgentV3ClosePhotoResources(t)
	entry, err := telegramPhotoDownloaders.acquire(f.bot)
	require.NoError(t, err)
	telegramPhotoDownloaders.release(entry)
	cached, _, _ := photoDownloaderLifecycleCounts()
	require.Equal(t, 1, cached)
	started, canceled := make(chan struct{}), make(chan struct{})
	repo := &agentSessionCollectionRepository{Repository: f.repo, scopes: func(ctx context.Context) ([]session.Scope, error) {
		close(started)
		<-ctx.Done()
		close(canceled)
		return nil, ctx.Err()
	}}
	files, err := session.NewFileStore(t.TempDir())
	require.NoError(t, err)
	service, err := session.NewService(repo, files, session.Options{})
	require.NoError(t, err)
	s := startAgentV3SessionMaintenance(t.Context(), service, time.Local)
	agentSessionService.Store(s)
	t.Cleanup(s.close)
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("maintenance did not recover on startup")
	}
	var workers sync.WaitGroup
	workers.Add(2)
	done := make(chan struct{}, 2)
	for range 2 {
		go func() { defer workers.Done(); Close(); done <- struct{}{} }()
	}
	for range 2 {
		select {
		case <-done:
			select {
			case <-canceled:
			default:
				t.Fatal("Close returned before collection cancellation")
			}
			select {
			case <-s.done:
			default:
				t.Fatal("Close returned before maintenance exit")
			}
		case <-time.After(3 * time.Second):
			t.Fatal("concurrent Close did not finish")
		}
	}
	workers.Wait()
	require.Nil(t, agentSessionService.Load())
	require.ErrorIs(t, service.Collect(t.Context()), session.ErrClosed)
	cached, retired, references := photoDownloaderLifecycleCounts()
	require.Zero(t, cached, "Close must retire photo transports after session termination")
	require.Zero(t, retired)
	require.Zero(t, references)
}

type agentSessionRecoveryRepository struct {
	session.Repository
	scope       session.Scope
	recoveries  atomic.Int32
	collections atomic.Int32
	collectedAt chan time.Time
}

func (r *agentSessionRecoveryRepository) Scopes(context.Context) ([]session.Scope, error) {
	return []session.Scope{r.scope}, nil
}

func (r *agentSessionRecoveryRepository) Pending(context.Context, session.Scope) ([]session.Intent, error) {
	return nil, nil
}

func (r *agentSessionRecoveryRepository) Deleting(context.Context, session.Scope) ([]session.Deletion, error) {
	if r.recoveries.Add(1) == 1 {
		return nil, errAgentSessionFixtureRecovery
	}
	return nil, nil
}

func (r *agentSessionRecoveryRepository) ClaimDeleting(context.Context, session.Scope, time.Duration) ([]session.Deletion, error) {
	r.collectedAt <- time.Now()
	if r.collections.Add(1) == 1 {
		return nil, errAgentSessionFixtureCollection
	}
	return nil, nil
}

func TestAgentV3SessionMaintenanceRecoversBetweenCalendarCollections(t *testing.T) {
	f := newAgentSessionFixture(t)
	files, err := session.NewFileStore(t.TempDir())
	require.NoError(t, err)
	synctest.Test(t, func(t *testing.T) {
		location := time.FixedZone("session-local", -3600)
		repo := &agentSessionRecoveryRepository{Repository: f.repo, scope: f.scope(), collectedAt: make(chan time.Time, 2)}
		service, err := session.NewService(repo, files, session.Options{})
		require.NoError(t, err)
		s := startAgentV3SessionMaintenance(t.Context(), service, location)
		defer s.close()
		synctest.Wait()
		require.EqualValues(t, 1, repo.recoveries.Load())
		require.Zero(t, repo.collections.Load())
		time.Sleep(time.Minute)
		synctest.Wait()
		require.EqualValues(t, 2, repo.recoveries.Load())
		require.Zero(t, repo.collections.Load(), "recovery retries must not claim ordinary expired DAGs")
		next, err := session.NextCollection(time.Now(), location)
		require.NoError(t, err)
		time.Sleep(time.Until(next))
		synctest.Wait()
		require.EqualValues(t, 1, repo.collections.Load())
		require.Equal(t, next, (<-repo.collectedAt).In(location))
		previousRecoveries := repo.recoveries.Load()
		time.Sleep(time.Minute)
		synctest.Wait()
		require.EqualValues(t, 1, repo.collections.Load(), "collection failure must not cause an off-schedule active DAG sweep")
		require.Greater(t, repo.recoveries.Load(), previousRecoveries)
		next, err = session.NextCollection(time.Now(), location)
		require.NoError(t, err)
		time.Sleep(time.Until(next))
		synctest.Wait()
		require.EqualValues(t, 2, repo.collections.Load())
		require.Equal(t, next, (<-repo.collectedAt).In(location))
	})
}

func TestAgentV3SessionBackgroundDoesNotLoadCaptureOrCommit(t *testing.T) {
	f := newAgentSessionFixture(t)
	cfg := &config.AgentConfig{Name: "background", ContextMode: "reply_chain", Session: config.AgentSessionConfig{LoadContext: true}}
	mdl := &scriptedToolModel{turns: [][]*schema.Message{{schema.AssistantMessage("background answer", nil)}}}
	compiled := f.compile(t, cfg, mdl)
	tc := &TurnContext{Bot: f.bot, BotUser: f.bot.Me, Message: sessionMessage(100, 7, 0, "background input"), ChatID: -100, Config: cfg, Background: true}
	ctx := WithTurnContext(t.Context(), tc)
	setupAgentV3SessionTurn(tc)
	require.Nil(t, tc.Session)
	messages, err := prepareAgentV3Turn(ctx, compiled, tc, nil)
	require.NoError(t, err)
	_, err = compiled.Agent.Generate(ctx, messages)
	require.NoError(t, err)
	commitAgentV3Session(tc, &tb.Message{ID: 42})
	scopes, err := f.repo.Scopes(t.Context())
	require.NoError(t, err)
	require.Empty(t, scopes)
}

type agentSessionReleaseRepository struct {
	session.Repository
	releases atomic.Int32
}

func (r *agentSessionReleaseRepository) Release(ctx context.Context, scope session.Scope, lease session.Lease) error {
	err := r.Repository.Release(ctx, scope, lease)
	r.releases.Add(1)
	return err
}

func TestAgentV3SessionParentReleasedOnEveryLoadedExit(t *testing.T) {
	for _, exit := range []string{"load only", "prepare error", "model error", "send error"} {
		t.Run(exit, func(t *testing.T) {
			f := newAgentSessionFixture(t)
			cfg := &config.AgentConfig{Name: "release", ContextMode: "chat"}
			mdl := &agentSessionModel{scriptedToolModel: &scriptedToolModel{turns: [][]*schema.Message{{schema.AssistantMessage("first final", nil)}, {schema.AssistantMessage("second final", nil)}}}}
			compiled := f.compile(t, cfg, mdl)
			f.chat(t, cfg, sessionMessage(100, 7, 0, "first input"), nil)
			repo := &agentSessionReleaseRepository{Repository: f.repo}
			files, err := session.NewFileStore(f.directory)
			require.NoError(t, err)
			service, err := session.NewService(repo, files, session.Options{})
			require.NoError(t, err)
			t.Cleanup(func() { _ = service.Close() })
			done := make(chan struct{})
			close(done)
			agentSessionService.Store(&agentV3SessionService{service: service, cancel: func() {}, done: done})
			noSave := false
			cfg.Session = config.AgentSessionConfig{SaveContext: &noSave, LoadContext: true}
			switch exit {
			case "prepare error":
				compiled.SystemTemplate = template.Must(template.New("invalid").Parse("{{.Input}}"))
			case "model error":
				mdl.before = func(context.Context, []*schema.Message) error { return errAgentSessionFixtureModel }
			case "send error":
				f.failDelivery = true
			}
			_ = Chat(f.bot.NewContext(tb.Update{Message: sessionMessage(200, 7, 60, "second input")}), cfg, nil)
			if exit == "prepare error" {
				require.Zero(t, repo.releases.Load(), "common-frame errors now occur before any pin is acquired")
			} else {
				require.EqualValues(t, 1, repo.releases.Load())
			}
		})
	}
}

func TestAgentV3SessionInitGatesAndRevokesOldService(t *testing.T) {
	for _, mode := range []string{"no agents", "agent disabled", "global disabled", "session disabled", "nil config", "bad directory"} {
		t.Run(mode, func(t *testing.T) {
			f := newAgentSessionFixture(t)
			old := agentSessionService.Load()
			oldManager, oldCron := mcpManager, cronService.Load()
			mcpManager = nil
			cronService.Store(nil)
			t.Cleanup(func() { Close(); mcpManager = oldManager; cronService.Store(oldCron) })
			cfg := &config.AgentConfig{Name: "enabled", Model: &config.Model{Model: "fixture", ApiKey: "fixture-key", BaseUrl: "http://model.invalid"}, Agent: &config.AgentOptions{Enable: true}}
			config.BotConfig.Agents = &config.AgentV3Configs{cfg}
			unused := filepath.Join(t.TempDir(), "must-not-be-created")
			config.BotConfig.AgentV3.Session.Directory = unused
			switch mode {
			case "no agents":
				config.BotConfig.Agents = nil
				compiledAgents.Store("stale", &CompiledAgent{Name: "stale"})
			case "agent disabled":
				cfg.Agent.Enable = false
			case "global disabled":
				config.BotConfig.AgentV3.Enable = false
			case "session disabled":
				enabled := false
				config.BotConfig.AgentV3.Session.Enable = &enabled
			case "nil config":
				config.BotConfig = nil
			case "bad directory":
				file := filepath.Join(t.TempDir(), "not-a-directory")
				require.NoError(t, os.WriteFile(file, []byte("occupied"), 0o600))
				config.BotConfig.AgentV3.Session.Directory = file
			}
			require.NoError(t, Init(t.Context()), "valid optional storage failure must not fail bot startup")
			require.Nil(t, agentSessionService.Load())
			require.ErrorIs(t, old.service.Recover(t.Context()), session.ErrClosed)
			select {
			case <-old.done:
			default:
				t.Fatal("old maintenance is still running")
			}
			_, err := os.Stat(unused)
			require.True(t, os.IsNotExist(err), "disabled session must not probe/create storage")
			if mode == "bad directory" {
				require.True(t, HasCompiledAgent(cfg.Name), "optional storage degradation must retain the enabled agent")
				f.compile(t, cfg, &scriptedToolModel{turns: [][]*schema.Message{{schema.AssistantMessage("fallback is usable", nil)}}})
				f.chat(t, cfg, sessionMessage(1100, 8, 60, "still works"), nil)
				scopes, err := f.repo.Scopes(t.Context())
				require.NoError(t, err)
				require.Empty(t, scopes, "degraded fallback cannot write the old session directory")
			}
		})
	}
}

func TestAgentV3SessionReloadClosesOldDirectory(t *testing.T) {
	f := newAgentSessionFixture(t)
	old := agentSessionService.Load()
	config.BotConfig.Agents = &config.AgentV3Configs{{Name: "enabled", Agent: &config.AgentOptions{Enable: true}}}
	config.BotConfig.AgentV3.Session.Directory = t.TempDir()
	require.NoError(t, initAgentV3SessionService(t.Context()))
	current := agentSessionService.Load()
	require.NotNil(t, current)
	t.Cleanup(closeAgentV3SessionService)
	require.NotSame(t, old, current)
	require.ErrorIs(t, old.service.Recover(t.Context()), session.ErrClosed)
	require.NoError(t, current.service.Recover(t.Context()))
	_, err := old.service.Commit(t.Context(), session.CommitRequest{Scope: f.scope()})
	require.ErrorIs(t, err, session.ErrClosed)
}

func TestAgentV3SessionInvalidConfigStillFailsWhenDisabled(t *testing.T) {
	newAgentSessionFixture(t)
	enabled := false
	config.BotConfig.AgentV3.Session.Enable = &enabled
	config.BotConfig.AgentV3.Session.ContextOverflow.MaxTokens = -1
	require.Error(t, initAgentV3SessionService(t.Context()))
	require.Nil(t, agentSessionService.Load())
}
