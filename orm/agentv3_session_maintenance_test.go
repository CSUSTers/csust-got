package orm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"csust-got/agent/session"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

type sessionMaintenanceHook struct {
	before func(context.Context, []redis.Cmder) error
	after  func(context.Context, []redis.Cmder) error
}

func (*sessionMaintenanceHook) DialHook(next redis.DialHook) redis.DialHook          { return next }
func (*sessionMaintenanceHook) ProcessHook(next redis.ProcessHook) redis.ProcessHook { return next }
func (h *sessionMaintenanceHook) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return func(ctx context.Context, cmds []redis.Cmder) error {
		if h.before != nil {
			if err := h.before(ctx, cmds); err != nil {
				return err
			}
		}
		err := next(ctx, cmds)
		if err == nil && h.after != nil {
			return h.after(ctx, cmds)
		}
		return err
	}
}

func sessionMaintenanceCommand(cmds []redis.Cmder, name, suffix string) bool {
	for _, cmd := range cmds {
		args := cmd.Args()
		if cmd.Name() == name && len(args) > 1 && strings.HasSuffix(fmt.Sprint(args[1]), suffix) {
			return true
		}
	}
	return false
}

func sessionPendingFixture(t *testing.T, roots int) (*sessionFixture, sessionState) {
	t.Helper()
	f := newSessionFixture(t, session.Options{})
	state := sessionBenchmarkState(f.scope, f.now.UnixMilli(), "roots", roots)
	sessionPublishedIntentFixture(state)
	sessionStoreFixture(t, f, state)
	return f, state
}

func TestAgentV3SessionClaimPrescreenNeverAuthorizesDeletion(t *testing.T) {
	for _, change := range []string{"fresh-to-due", "due-to-fresh", "due-lease", "due-deleting"} {
		t.Run(change, func(t *testing.T) {
			f := newSessionFixture(t, session.Options{TTL: time.Hour})
			node, err := f.service.Commit(t.Context(), sessionRequest(t, f.scope, "A", nil, 101, "guarded archive"))
			require.NoError(t, err)
			if change != "fresh-to-due" {
				f.mr.SetTime(f.now.Add(2 * time.Hour))
			}
			s, _ := f.repo.scopeKeys(f.scope)
			key := s.dag(node.Ref.DAGID).meta
			other := sessionOtherRepository(t, f)
			var once sync.Once
			var competing []session.Deletion
			f.repo.client.AddHook(&sessionMaintenanceHook{after: func(ctx context.Context, cmds []redis.Cmder) error {
				if !sessionMaintenanceCommand(cmds, "get", ":meta") || sessionMaintenanceCommand(cmds, "hgetall", ":intents") {
					return nil
				}
				var actionErr error
				once.Do(func() {
					switch change {
					case "fresh-to-due", "due-to-fresh":
						var meta sessionMeta
						data, err := other.client.Get(ctx, key).Bytes()
						if err != nil {
							actionErr = err
							return
						}
						if err := json.Unmarshal(data, &meta); err != nil {
							actionErr = err
							return
						}
						meta.LastActive = f.now.Add(-2 * time.Hour).UnixMilli()
						if change == "due-to-fresh" {
							meta.LastActive = f.now.Add(2 * time.Hour).UnixMilli()
						}
						actionErr = other.client.Set(ctx, key, sessionEncode(meta), 0).Err()
					case "due-lease":
						_, actionErr = other.ResolveAndPin(ctx, session.Selection{Scope: f.scope, Mode: session.SelectReply, ReplyMessageID: 101}, sessionRun(t), time.Hour)
					case "due-deleting":
						competing, actionErr = other.ClaimDeleting(ctx, f.scope, time.Hour)
					}
				})
				return actionErr
			}})
			claimed, err := f.repo.ClaimDeleting(t.Context(), f.scope, time.Hour)
			require.NoError(t, err)
			if change == "due-deleting" {
				require.ElementsMatch(t, competing, claimed)
				require.Len(t, claimed, 1)
			} else {
				require.Empty(t, claimed)
				require.Equal(t, sessionDAGActive, sessionReadState(t, f).DAGs[node.Ref.DAGID].State)
			}
			_, err = os.Stat(sessionArchivePath(f.dir, node))
			require.NoError(t, err)
			if change == "fresh-to-due" {
				claimed, err = f.repo.ClaimDeleting(t.Context(), f.scope, time.Hour)
				require.NoError(t, err)
				require.Len(t, claimed, 1, "deferred candidate is reconsidered by the next fresh scan")
			}
		})
	}
}

func TestAgentV3SessionPendingBatchRetryResetsResultsAndErrors(t *testing.T) {
	f, state := sessionPendingFixture(t, 2)
	s, _ := f.repo.scopeKeys(f.scope)
	a := state.DAGs[fmt.Sprintf("%032x", 1)]
	b := state.DAGs[fmt.Sprintf("%032x", 2)]
	var repaired session.Intent
	for _, i := range a.Intents {
		repaired = i
	}
	var removed session.Intent
	for _, i := range b.Intents {
		removed = i
	}
	require.NoError(t, f.repo.client.HSet(t.Context(), s.dag(a.ID).intents, repaired.Node.RunID, "private corrupt intent").Err())
	other := sessionOtherRepository(t, f)
	counter := sessionCountCommands(t, f)
	ready := false
	var once sync.Once
	f.repo.client.AddHook(&sessionMaintenanceHook{before: func(ctx context.Context, cmds []redis.Cmder) error {
		if sessionMaintenanceCommand(cmds, "hgetall", ":intents") {
			ready = true
		}
		if !ready || len(cmds) == 0 || cmds[0].Name() != "multi" {
			return nil
		}
		var err error
		once.Do(func() {
			err = other.client.HSet(ctx, s.dag(a.ID).intents, repaired.Node.RunID, sessionEncode(repaired)).Err()
			if err == nil {
				err = other.client.HDel(ctx, s.dag(b.ID).intents, removed.Node.RunID).Err()
			}
		})
		return err
	}})
	intents, err := f.repo.Pending(t.Context(), f.scope)
	require.NoError(t, err)
	require.Equal(t, []session.Intent{repaired}, intents)
	require.EqualValues(t, 1, counter.snapshot().conflicts)
}

func TestAgentV3SessionPendingBatchIsolatesWrongTypes(t *testing.T) {
	f, state := sessionPendingFixture(t, 3)
	s, _ := f.repo.scopeKeys(f.scope)
	a := state.DAGs[fmt.Sprintf("%032x", 1)]
	b := state.DAGs[fmt.Sprintf("%032x", 2)]
	require.NoError(t, f.repo.client.Del(t.Context(), s.dag(a.ID).intents).Err())
	require.NoError(t, f.repo.client.Set(t.Context(), s.dag(a.ID).intents, "private wrong type", 0).Err())
	require.NoError(t, f.repo.client.Set(t.Context(), s.dag(b.ID).meta, "private malformed meta", 0).Err())
	intents, err := f.repo.Pending(t.Context(), f.scope)
	require.ErrorIs(t, err, session.ErrCorrupt)
	require.True(t, sessionPureCorruption(err))
	require.Len(t, intents, 1)
	require.NotContains(t, err.Error(), "private")
	require.Equal(t, "private wrong type", f.repo.client.Get(t.Context(), s.dag(a.ID).intents).Val())
}

func TestAgentV3SessionPendingBatchOperationalFailureDropsPriorResults(t *testing.T) {
	for _, failure := range []string{"transport", "mixed", "cancel", "conflict"} {
		t.Run(failure, func(t *testing.T) {
			f, _ := sessionPendingFixture(t, sessionPendingBatchSize+1)
			s, _ := f.repo.scopeKeys(f.scope)
			other := sessionOtherRepository(t, f)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			batches := 0
			ready := false
			f.repo.client.AddHook(&sessionMaintenanceHook{before: func(ctx context.Context, cmds []redis.Cmder) error {
				if sessionMaintenanceCommand(cmds, "hgetall", ":intents") {
					batches++
					ready = batches >= 2
					if batches >= 2 {
						switch failure {
						case "transport":
							return errSessionDisconnected
						case "mixed":
							return errors.Join(session.ErrCorrupt, errSessionDisconnected)
						case "cancel":
							cancel()
							return ctx.Err()
						}
					}
				}
				if failure == "conflict" && ready && len(cmds) > 0 && cmds[0].Name() == "multi" {
					return other.client.SAdd(ctx, s.dags, sessionRun(t)).Err()
				}
				return nil
			}})
			intents, err := f.repo.Pending(ctx, f.scope)
			require.Nil(t, intents)
			switch failure {
			case "transport", "mixed":
				require.ErrorIs(t, err, errSessionDisconnected)
				require.NotErrorIs(t, err, session.ErrCorrupt, "consumer must not classify operational failure as resumable corruption")
			case "cancel":
				require.ErrorIs(t, err, context.Canceled)
				require.Equal(t, 2, batches)
			case "conflict":
				require.ErrorIs(t, err, session.ErrConflict)
			}
		})
	}
}

func TestAgentV3SessionRecoverManyPublishedAndFewPending(t *testing.T) {
	f := newSessionFixture(t, session.Options{})
	state := sessionPartitionFixture(t, f, "roots", 1000)
	sessionPublishedIntentFixture(state)
	sessionRestoreFixture(t, f, state)
	files, err := session.NewFileStore(f.dir)
	require.NoError(t, err)
	defer files.Close()
	pending := make([]session.Intent, 0, 3)
	for range 3 {
		i, err := f.repo.Reserve(t.Context(), session.Reservation{Scope: f.scope, Agent: "A", RunID: sessionRun(t)}, time.Hour)
		require.NoError(t, err)
		sessionWriteMaintenanceArchive(t, files, i.Node, sessionBenchmarkCapture())
		pending = append(pending, i)
	}
	require.NoError(t, f.service.Recover(t.Context()))
	view := sessionReadState(t, f)
	for id, dag := range state.DAGs {
		require.Equal(t, dag.Nodes, view.DAGs[id].Nodes)
		require.Empty(t, view.DAGs[id].Intents)
	}
	for _, i := range pending {
		require.Empty(t, view.DAGs[i.Node.Ref.DAGID].Intents)
		_, err := os.Stat(sessionArchivePath(f.dir, i.Node))
		require.ErrorIs(t, err, os.ErrNotExist)
	}
}

func TestAgentV3SessionMaintenanceBudgetAndCloseStopNewBatches(t *testing.T) {
	for _, stop := range []string{"budget", "close"} {
		t.Run(stop, func(t *testing.T) {
			f, _ := sessionPendingFixture(t, sessionPendingBatchSize+1)
			svc := fixtureService(t, f.repo, f.dir, session.Options{OperationTimeout: 50 * time.Millisecond})
			entered := make(chan struct{})
			var once sync.Once
			reads := 0
			f.repo.client.AddHook(&sessionMaintenanceHook{before: func(ctx context.Context, cmds []redis.Cmder) error {
				if !sessionMaintenanceCommand(cmds, "hgetall", ":intents") {
					return nil
				}
				reads++
				once.Do(func() { close(entered) })
				<-ctx.Done()
				return ctx.Err()
			}})
			done := make(chan error, 1)
			go func() { done <- svc.Recover(t.Context()) }()
			<-entered
			if stop == "close" {
				require.NoError(t, svc.Close())
			}
			err := <-done
			if stop == "budget" {
				require.ErrorIs(t, err, context.DeadlineExceeded)
			} else {
				require.ErrorIs(t, err, context.Canceled)
			}
			require.Equal(t, 1, reads, "no next batch or recovery action after cancellation")
			require.NoError(t, svc.Close())
			require.ErrorIs(t, svc.Recover(t.Context()), session.ErrClosed)
			require.Equal(t, 1, reads)
		})
	}
}

func TestAgentV3SessionPendingBatchRechecksGenerationAndState(t *testing.T) {
	for _, change := range []string{"generation", "state"} {
		t.Run(change, func(t *testing.T) {
			f, state := sessionPendingFixture(t, 1)
			s, _ := f.repo.scopeKeys(f.scope)
			id := fmt.Sprintf("%032x", 1)
			other := sessionOtherRepository(t, f)
			ready := false
			var once sync.Once
			f.repo.client.AddHook(&sessionMaintenanceHook{before: func(ctx context.Context, cmds []redis.Cmder) error {
				if sessionMaintenanceCommand(cmds, "hgetall", ":intents") {
					ready = true
				}
				if !ready || len(cmds) == 0 || cmds[0].Name() != "multi" {
					return nil
				}
				var err error
				once.Do(func() {
					key := s.dag(id).meta
					var meta sessionMeta
					data, readErr := other.client.Get(ctx, key).Bytes()
					if readErr != nil {
						err = readErr
						return
					}
					if err = json.Unmarshal(data, &meta); err != nil {
						return
					}
					if change == "generation" {
						meta.Generation = sessionRun(t)
					} else {
						meta.State = sessionDAGDeleting
					}
					err = other.client.Set(ctx, key, sessionEncode(meta), 0).Err()
				})
				return err
			}})
			intents, err := f.repo.Pending(t.Context(), f.scope)
			require.Empty(t, intents)
			if change == "generation" {
				require.ErrorIs(t, err, session.ErrCorrupt)
			} else {
				require.NoError(t, err)
			}
			require.EqualValues(t, len(state.DAGs[id].Intents), f.repo.client.HLen(t.Context(), s.dag(id).intents).Val(), "readonly scan must not clear frozen or mismatched metadata")
		})
	}
}

func TestAgentV3SessionPendingCancellationAfterValidatedBatchReturnsNoResults(t *testing.T) {
	f, _ := sessionPendingFixture(t, 1)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	ready := false
	f.repo.client.AddHook(&sessionMaintenanceHook{after: func(_ context.Context, cmds []redis.Cmder) error {
		if sessionMaintenanceCommand(cmds, "hgetall", ":intents") {
			ready = true
		}
		if ready && len(cmds) > 0 && cmds[0].Name() == "multi" {
			cancel()
		}
		return nil
	}})
	intents, err := f.repo.Pending(ctx, f.scope)
	require.ErrorIs(t, err, context.Canceled)
	require.Nil(t, intents)
}
