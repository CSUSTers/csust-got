package orm

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"csust-got/agent/session"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestAgentV3SessionKnownScopesDoNotRewriteCatalogOrConflict(t *testing.T) {
	for _, fresh := range []bool{false, true} {
		t.Run(strconv.FormatBool(fresh), func(t *testing.T) {
			f := newSessionFixture(t, session.Options{})
			_, _ = sessionRootIntent(t, f, 101)
			otherScope := f.scope
			otherScope.ChatID++
			other := sessionOtherRepository(t, f)
			if !fresh {
				_, err := other.Reserve(t.Context(), session.Reservation{Scope: otherScope, Agent: "A", RunID: sessionRun(t)}, time.Hour)
				require.NoError(t, err)
			}
			counter := sessionCountCommands(t, f)
			counter.trace = true
			f.repo.client.AddHook(&sessionBeforeExecHook{run: func(ctx context.Context) error {
				_, err := other.Reserve(ctx, session.Reservation{Scope: otherScope, Agent: "A", RunID: sessionRun(t)}, time.Hour)
				return err
			}})
			_, err := f.repo.Reserve(t.Context(), session.Reservation{Scope: f.scope, Agent: "A", RunID: sessionRun(t)}, time.Hour)
			require.NoError(t, err)
			stats := counter.snapshot()
			require.Zero(t, stats.writeValues["catalog"])
			for _, event := range counter.events {
				if event.key == f.repo.base+"scopes" {
					require.NotEqual(t, "hset", event.name)
				}
			}
			if fresh {
				require.EqualValues(t, 1, stats.conflicts, "first registration still changes the shared watched catalog")
			} else {
				require.Zero(t, stats.conflicts, "known different scopes share no written WATCH key")
			}
		})
	}
}

func TestAgentV3SessionRootRetriesFinalScopeCleanup(t *testing.T) {
	f := newSessionFixture(t, session.Options{})
	_, _ = sessionRootIntent(t, f, 101)
	f.mr.SetTime(f.now.Add(2 * time.Hour))
	deletions, err := f.repo.ClaimDeleting(t.Context(), f.scope, time.Hour)
	require.NoError(t, err)
	require.Len(t, deletions, 1)
	other := sessionOtherRepository(t, f)
	counter := sessionCountCommands(t, f)
	f.repo.client.AddHook(&sessionBeforeExecHook{run: func(ctx context.Context) error {
		return other.FinishDelete(ctx, f.scope, deletions[0])
	}})
	i, err := f.repo.Reserve(t.Context(), session.Reservation{Scope: f.scope, Agent: "A", RunID: sessionRun(t)}, time.Hour)
	require.NoError(t, err)
	require.EqualValues(t, 1, counter.snapshot().conflicts)
	s, _ := f.repo.scopeKeys(f.scope)
	require.Equal(t, []string{i.Node.Ref.DAGID}, f.repo.client.SMembers(t.Context(), s.dags).Val())
	require.Equal(t, []string{i.Node.Ref.DAGID}, f.repo.client.SMembers(t.Context(), s.pending).Val())
	require.Equal(t, "0", f.repo.client.Get(t.Context(), s.sequence).Val())
	require.Equal(t, sessionEncode(f.scope), f.repo.client.HGet(t.Context(), f.repo.base+"scopes", f.scope.Key()).Val())
	require.NotEqual(t, deletions[0].Generation, i.Lease.Generation)
}

func TestAgentV3SessionPendingIndexTracksDurableIntentLifecycle(t *testing.T) {
	f := newSessionFixture(t, session.Options{})
	s, _ := f.repo.scopeKeys(f.scope)
	req := session.Reservation{Scope: f.scope, Agent: "A", RunID: sessionRun(t)}
	i, err := f.repo.Reserve(t.Context(), req, time.Hour)
	require.NoError(t, err)
	d := s.dag(i.Node.Ref.DAGID)
	require.True(t, f.repo.client.SIsMember(t.Context(), s.pending, d.id).Val())
	_, err = f.repo.Publish(t.Context(), f.scope, i, strings.Repeat("a", 64), 100, session.DeliveryReceipt{MessageIDs: []int{101}})
	require.NoError(t, err)
	intents, err := f.repo.Pending(t.Context(), f.scope)
	require.NoError(t, err)
	require.Len(t, intents, 1)
	require.Equal(t, sessionIntentPublished, intents[0].Status, "publication does not finish recovery metadata")
	require.NoError(t, f.repo.FinishIntent(t.Context(), f.scope, i))
	require.Zero(t, f.repo.client.SCard(t.Context(), s.pending).Val())

	i, err = f.repo.Reserve(t.Context(), session.Reservation{Scope: f.scope, Agent: "A", RunID: sessionRun(t)}, time.Hour)
	require.NoError(t, err)
	aborted, err := f.repo.AbortIntent(t.Context(), f.scope, i)
	require.NoError(t, err)
	require.True(t, aborted)
	intents, err = f.repo.Pending(t.Context(), f.scope)
	require.NoError(t, err)
	require.Equal(t, sessionIntentAborted, intents[0].Status, "compensation must remain discoverable until FinishIntent")
	require.NoError(t, f.repo.FinishIntent(t.Context(), f.scope, i))
	require.Zero(t, f.repo.client.SCard(t.Context(), s.pending).Val())

	i, err = f.repo.Reserve(t.Context(), session.Reservation{Scope: f.scope, Agent: "A", RunID: sessionRun(t)}, time.Minute)
	require.NoError(t, err)
	f.mr.SetTime(f.now.Add(2 * time.Hour))
	deletions, err := f.repo.ClaimDeleting(t.Context(), f.scope, time.Hour)
	require.NoError(t, err)
	require.Len(t, deletions, 3)
	require.Zero(t, f.repo.client.SCard(t.Context(), s.pending).Val(), "handoff to the frozen deleting manifest is atomic")
	require.EqualValues(t, 3, f.repo.client.SCard(t.Context(), s.deleting).Val())
	reopened := sessionOtherRepository(t, f)
	intents, err = reopened.Pending(t.Context(), f.scope)
	require.NoError(t, err)
	require.Empty(t, intents)
	resumed, err := reopened.Deleting(t.Context(), f.scope)
	require.NoError(t, err)
	require.ElementsMatch(t, deletions, resumed, "crash restart retains the unpublished intent in its frozen manifest")
	for _, deletion := range resumed {
		require.NoError(t, reopened.FinishDelete(t.Context(), f.scope, deletion))
	}
	for _, key := range []string{s.dags, s.pending, s.deleting, s.runs, s.sequence, s.messages} {
		require.Equal(t, sessionRedisNone, f.repo.client.Type(t.Context(), key).Val(), key)
	}
	require.Empty(t, f.repo.client.HGetAll(t.Context(), f.repo.base+"scopes").Val())
}

func TestAgentV3SessionFinishLastIntentRetriesConcurrentChildReservation(t *testing.T) {
	f := newSessionFixture(t, session.Options{})
	_, root := sessionRootIntent(t, f, 101)
	pin, err := f.repo.ResolveAndPin(t.Context(), session.Selection{Scope: f.scope, Mode: session.SelectReply, ReplyMessageID: 101}, sessionRun(t), time.Hour)
	require.NoError(t, err)
	i, err := f.repo.Reserve(t.Context(), session.Reservation{Scope: f.scope, Agent: "A", RunID: sessionRun(t), Parent: &root.Ref, Lease: &pin.Lease}, time.Hour)
	require.NoError(t, err)
	aborted, err := f.repo.AbortIntent(t.Context(), f.scope, i)
	require.NoError(t, err)
	require.True(t, aborted)
	other := sessionOtherRepository(t, f)
	counter := sessionCountCommands(t, f)
	var competing session.Intent
	f.repo.client.AddHook(&sessionBeforeExecHook{run: func(ctx context.Context) error {
		var err error
		competing, err = other.Reserve(ctx, session.Reservation{Scope: f.scope, Agent: "B", RunID: sessionRun(t), Parent: &root.Ref, Lease: &pin.Lease}, time.Hour)
		return err
	}})
	require.NoError(t, f.repo.FinishIntent(t.Context(), f.scope, i))
	require.EqualValues(t, 1, counter.snapshot().conflicts)
	intents, err := f.repo.Pending(t.Context(), f.scope)
	require.NoError(t, err)
	require.Equal(t, []session.Intent{competing}, intents)
}

func TestAgentV3SessionEmptyRecoveryRPCIsIndependentOfHealthyRoots(t *testing.T) {
	var baseline sessionCommandStats
	for _, roots := range []int{1, 100, 1000} {
		t.Run(strconv.Itoa(roots), func(t *testing.T) {
			f := newSessionFixture(t, session.Options{})
			sessionStoreFixture(t, f, sessionBenchmarkState(f.scope, f.now.UnixMilli(), "roots", roots))
			counter := sessionCountCommands(t, f)
			counter.trace = true
			require.NoError(t, f.service.Recover(t.Context()))
			stats := counter.snapshot()
			require.Zero(t, stats.commands["get"], "no ordinary DAG metadata reads")
			require.EqualValues(t, 1, stats.commands["hgetall"], "only the scope catalog is read in full")
			s, _ := f.repo.scopeKeys(f.scope)
			for _, event := range counter.events {
				require.NotEqual(t, s.dags, event.key)
			}
			if roots == 1 {
				baseline = stats
			} else {
				require.Equal(t, baseline.commands, stats.commands)
				require.Equal(t, baseline.readValues, stats.readValues)
				require.Equal(t, baseline.pipelines, stats.pipelines)
			}
		})
	}
}

func TestAgentV3SessionRecoveryIndexesRetainMissingCorruptAndUnknownData(t *testing.T) {
	for _, kind := range []string{"pending", "deleting"} {
		for _, fault := range []string{"missing-index", "index-type", "missing-meta", "unknown-member", "invalid-member", "empty-intents"} {
			if kind == "deleting" && fault == "empty-intents" {
				continue
			}
			t.Run(kind+"/"+fault, func(t *testing.T) {
				f := newSessionFixture(t, session.Options{})
				req := session.Reservation{Scope: f.scope, Agent: "A", RunID: sessionRun(t)}
				i, err := f.repo.Reserve(t.Context(), req, time.Minute)
				require.NoError(t, err)
				s, _ := f.repo.scopeKeys(f.scope)
				d := s.dag(i.Node.Ref.DAGID)
				index := s.pending
				if kind == "deleting" {
					f.mr.SetTime(f.now.Add(2 * time.Hour))
					deletions, err := f.repo.ClaimDeleting(t.Context(), f.scope, time.Hour)
					require.NoError(t, err)
					require.Len(t, deletions, 1)
					index = s.deleting
				}
				switch fault {
				case "missing-index":
					require.NoError(t, f.repo.client.Del(t.Context(), index).Err())
				case "index-type":
					require.NoError(t, f.repo.client.Del(t.Context(), index).Err())
					require.NoError(t, f.repo.client.Set(t.Context(), index, "private wrong type", 0).Err())
				case "missing-meta":
					require.NoError(t, f.repo.client.Del(t.Context(), d.meta).Err())
				case "unknown-member":
					require.NoError(t, f.repo.client.SAdd(t.Context(), index, sessionRun(t)).Err())
				case "invalid-member":
					require.NoError(t, f.repo.client.SAdd(t.Context(), index, "invalid\nmember").Err())
				case "empty-intents":
					require.NoError(t, f.repo.client.Del(t.Context(), d.intents).Err())
				}
				before := f.repo.client.HGetAll(t.Context(), d.intents).Val()
				counter := sessionCountCommands(t, f)
				if kind == "pending" {
					_, err = f.repo.Pending(t.Context(), f.scope)
				} else {
					_, err = f.repo.Deleting(t.Context(), f.scope)
				}
				if fault == "missing-index" {
					require.NoError(t, err, "missing SET is indistinguishable from an empty index; do not scan or delete DAGs")
				} else {
					require.ErrorIs(t, err, session.ErrCorrupt)
					require.True(t, sessionPureCorruption(err))
				}
				for _, command := range []string{"del", "hdel", "srem", "sadd", "set", "hset"} {
					require.Zero(t, counter.snapshot().commands[command])
				}
				require.Equal(t, before, f.repo.client.HGetAll(t.Context(), d.intents).Val())
				require.True(t, f.repo.client.SIsMember(t.Context(), s.dags, d.id).Val())
				if fault == "missing-index" {
					if kind == "pending" {
						repeated, err := f.repo.Reserve(t.Context(), req, time.Minute)
						require.NoError(t, err)
						require.Equal(t, i, repeated)
					} else {
						deletions, err := f.repo.ClaimDeleting(t.Context(), f.scope, time.Hour)
						require.NoError(t, err)
						require.Len(t, deletions, 1)
						require.Equal(t, i.Lease.Generation, deletions[0].Generation)
					}
					require.True(t, f.repo.client.SIsMember(t.Context(), index, d.id).Val())
				}
			})
		}
	}
}

func TestAgentV3SessionFinalScopeCleanupRetainsUnknownRecoveryMembers(t *testing.T) {
	for _, kind := range []string{"pending", "deleting"} {
		t.Run(kind, func(t *testing.T) {
			f := newSessionFixture(t, session.Options{})
			_, _ = sessionRootIntent(t, f, 101)
			f.mr.SetTime(f.now.Add(2 * time.Hour))
			deletions, err := f.repo.ClaimDeleting(t.Context(), f.scope, time.Hour)
			require.NoError(t, err)
			s, _ := f.repo.scopeKeys(f.scope)
			index := s.pending
			if kind == "deleting" {
				index = s.deleting
			}
			unknown := sessionRun(t)
			require.NoError(t, f.repo.client.SAdd(t.Context(), index, unknown).Err())
			require.ErrorIs(t, f.repo.FinishDelete(t.Context(), f.scope, deletions[0]), session.ErrCorrupt)
			require.True(t, f.repo.client.SIsMember(t.Context(), index, unknown).Val())
			require.Equal(t, sessionEncode(f.scope), f.repo.client.HGet(t.Context(), f.repo.base+"scopes", f.scope.Key()).Val())
			require.True(t, f.repo.client.SIsMember(t.Context(), s.dags, deletions[0].DAGID).Val())
		})
	}
}

func TestAgentV3SessionReserveIndexesShareIntentEXEC(t *testing.T) {
	f := newSessionFixture(t, session.Options{})
	s, _ := f.repo.scopeKeys(f.scope)
	observed := false
	f.repo.client.AddHook(&sessionMaintenanceHook{before: func(_ context.Context, cmds []redis.Cmder) error {
		if len(cmds) == 0 || cmds[0].Name() != "multi" {
			return nil
		}
		if sessionMaintenanceCommand(cmds, "hset", ":intents") {
			observed = true
			require.True(t, sessionMaintenanceCommand(cmds, "sadd", ":pending_dags"))
			require.True(t, sessionMaintenanceCommand(cmds, "sadd", ":dags"))
		}
		return nil
	}})
	i, err := f.repo.Reserve(t.Context(), session.Reservation{Scope: f.scope, Agent: "A", RunID: sessionRun(t)}, time.Hour)
	require.NoError(t, err)
	require.True(t, observed)
	require.True(t, f.repo.client.SIsMember(t.Context(), s.pending, i.Node.Ref.DAGID).Val())
}

func TestAgentV3SessionDeletingBatchRetryDropsRemovedManifest(t *testing.T) {
	f := newSessionFixture(t, session.Options{})
	_, _ = sessionRootIntent(t, f, 101)
	_, _ = sessionRootIntent(t, f, 102)
	f.mr.SetTime(f.now.Add(2 * time.Hour))
	claimed, err := f.repo.ClaimDeleting(t.Context(), f.scope, time.Hour)
	require.NoError(t, err)
	require.Len(t, claimed, 2)
	other := sessionOtherRepository(t, f)
	counter := sessionCountCommands(t, f)
	f.repo.client.AddHook(&sessionBeforeExecHook{run: func(ctx context.Context) error {
		return other.FinishDelete(ctx, f.scope, claimed[0])
	}, match: func(cmds []redis.Cmder) bool {
		return counter.snapshot().commands["get"] > 0 && len(cmds) > 0
	}})
	deletions, err := f.repo.Deleting(t.Context(), f.scope)
	require.NoError(t, err)
	require.Equal(t, []session.Deletion{claimed[1]}, deletions)
	require.EqualValues(t, 1, counter.snapshot().conflicts)
}

func TestAgentV3SessionDeletingBatchRetryReturnsFreshGeneration(t *testing.T) {
	f := newSessionFixture(t, session.Options{})
	_, _ = sessionRootIntent(t, f, 101)
	f.mr.SetTime(f.now.Add(2 * time.Hour))
	claimed, err := f.repo.ClaimDeleting(t.Context(), f.scope, time.Hour)
	require.NoError(t, err)
	require.Len(t, claimed, 1)
	s, _ := f.repo.scopeKeys(f.scope)
	d := s.dag(claimed[0].DAGID)
	other := sessionOtherRepository(t, f)
	counter := sessionCountCommands(t, f)
	fresh := sessionRun(t)
	f.repo.client.AddHook(&sessionBeforeExecHook{run: func(ctx context.Context) error {
		var meta sessionMeta
		found, err := sessionReadJSON(ctx, other.client.Get(ctx, d.meta), &meta)
		if err != nil {
			return err
		}
		require.True(t, found)
		meta.Generation = fresh
		return other.client.Set(ctx, d.meta, sessionEncode(meta), 0).Err()
	}, match: func(cmds []redis.Cmder) bool {
		// The discovery EXEC has no DAG reads; mutate only the manifest snapshot.
		return counter.snapshot().commands["get"] > 0 && len(cmds) > 0
	}})
	deletions, err := f.repo.Deleting(t.Context(), f.scope)
	require.NoError(t, err)
	require.Len(t, deletions, 1)
	require.Equal(t, fresh, deletions[0].Generation)
	require.EqualValues(t, 1, counter.snapshot().conflicts)
	require.ErrorIs(t, f.repo.FinishDelete(t.Context(), f.scope, claimed[0]), session.ErrFence)
}

func TestAgentV3SessionDeletingBatchOperationalFailureDropsPriorResults(t *testing.T) {
	for _, failure := range []string{"transport", "mixed", "cancel", "conflict"} {
		t.Run(failure, func(t *testing.T) {
			f := newSessionFixture(t, session.Options{})
			state := sessionBenchmarkState(f.scope, f.now.UnixMilli(), "roots", sessionPendingBatchSize+1)
			for _, dag := range state.DAGs {
				dag.State = sessionDAGDeleting
			}
			sessionStoreFixture(t, f, state)
			other := sessionOtherRepository(t, f)
			s, _ := f.repo.scopeKeys(f.scope)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			reads, ready := 0, false
			f.repo.client.AddHook(&sessionMaintenanceHook{before: func(ctx context.Context, cmds []redis.Cmder) error {
				if sessionMaintenanceCommand(cmds, "hgetall", ":leases") {
					reads++
					if reads > sessionPendingBatchSize {
						ready = true
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
					return other.client.SAdd(ctx, s.deleting, sessionRun(t)).Err()
				}
				return nil
			}})
			deletions, err := f.repo.Deleting(ctx, f.scope)
			require.Nil(t, deletions)
			switch failure {
			case "transport", "mixed":
				require.ErrorIs(t, err, errSessionDisconnected)
				require.NotErrorIs(t, err, session.ErrCorrupt)
			case "cancel":
				require.ErrorIs(t, err, context.Canceled)
			case "conflict":
				require.ErrorIs(t, err, session.ErrConflict)
			}
		})
	}
}

func TestAgentV3SessionRecoveryIndexWrongTypesFenceMutations(t *testing.T) {
	for _, operation := range []string{"reserve", "publish", "abort", "finish", "claim", "delete"} {
		t.Run(operation, func(t *testing.T) {
			f := newSessionFixture(t, session.Options{})
			i, err := f.repo.Reserve(t.Context(), session.Reservation{Scope: f.scope, Agent: "A", RunID: sessionRun(t)}, time.Minute)
			require.NoError(t, err)
			var deletion session.Deletion
			if operation == "finish" {
				_, err := f.repo.AbortIntent(t.Context(), f.scope, i)
				require.NoError(t, err)
			}
			if operation == "claim" || operation == "delete" {
				f.mr.SetTime(f.now.Add(2 * time.Hour))
			}
			if operation == "delete" {
				deletions, err := f.repo.ClaimDeleting(t.Context(), f.scope, time.Hour)
				require.NoError(t, err)
				deletion = deletions[0]
			}
			s, _ := f.repo.scopeKeys(f.scope)
			index := s.pending
			if operation == "claim" || operation == "delete" {
				index = s.deleting
			}
			require.NoError(t, f.repo.client.Del(t.Context(), index).Err())
			require.NoError(t, f.repo.client.Set(t.Context(), index, "private wrong type", 0).Err())
			counter := sessionCountCommands(t, f)
			switch operation {
			case "reserve":
				_, err = f.repo.Reserve(t.Context(), session.Reservation{Scope: f.scope, Agent: "A", RunID: sessionRun(t)}, time.Minute)
			case "publish":
				_, err = f.repo.Publish(t.Context(), f.scope, i, strings.Repeat("a", 64), 10, session.DeliveryReceipt{MessageIDs: []int{101}})
			case "abort":
				_, err = f.repo.AbortIntent(t.Context(), f.scope, i)
			case "finish":
				err = f.repo.FinishIntent(t.Context(), f.scope, i)
			case "claim":
				_, err = f.repo.ClaimDeleting(t.Context(), f.scope, time.Hour)
			case "delete":
				err = f.repo.FinishDelete(t.Context(), f.scope, deletion)
			}
			require.ErrorIs(t, err, session.ErrCorrupt)
			for _, name := range []string{"hset", "set", "del", "hdel", "sadd", "srem", "zadd", "zrem"} {
				require.Zero(t, counter.snapshot().commands[name], name)
			}
			require.Equal(t, "private wrong type", f.repo.client.Get(t.Context(), index).Val())
		})
	}
}

func TestAgentV3SessionFinishedRootCorruptionIsOutsidePendingSnapshot(t *testing.T) {
	f := newSessionFixture(t, session.Options{})
	bad, err := f.service.Commit(t.Context(), sessionRequest(t, f.scope, "A", nil, 101, "finished root"))
	require.NoError(t, err)
	s, _ := f.repo.scopeKeys(f.scope)
	d := s.dag(bad.Ref.DAGID)
	indexed := f.repo.client.SIsMember(t.Context(), s.pending, d.id).Val()
	intentCount := f.repo.client.HLen(t.Context(), d.intents).Val()
	require.False(t, indexed)
	require.Zero(t, intentCount)
	t.Logf("after Commit: bad pending membership=%t, intent HLEN=%d", indexed, intentCount)
	require.NoError(t, f.repo.client.Set(t.Context(), d.meta, "malformed meta", 0).Err())
	for range 3 {
		_, err := f.repo.Reserve(t.Context(), session.Reservation{Scope: f.scope, Agent: "A", RunID: sessionRun(t)}, time.Hour)
		require.NoError(t, err)
	}
	counter := sessionCountCommands(t, f)
	counter.trace = true
	for _, includeBad := range []bool{false, true, false} {
		if includeBad {
			require.NoError(t, f.repo.client.SAdd(t.Context(), s.pending, d.id).Err())
		} else {
			require.NoError(t, f.repo.client.SRem(t.Context(), s.pending, d.id).Err())
		}
		counter.reset()
		intents, err := f.repo.Pending(t.Context(), f.scope)
		require.Len(t, intents, 3)
		if includeBad {
			require.ErrorIs(t, err, session.ErrCorrupt)
			require.True(t, sessionPureCorruption(err))
		} else {
			require.NoError(t, err)
			for _, event := range counter.events {
				require.NotEqual(t, d.meta, event.key, "unindexed finished root is not a minute recovery target")
			}
		}
		execs := counter.snapshot().commands["exec"]
		require.EqualValues(t, 2, execs, "both index discovery and the batch snapshot are WATCH/EXEC validated")
		t.Logf("include bad=%t: healthy intents=%d, error=%v, EXEC=%d", includeBad, len(intents), err, execs)
	}
	require.Equal(t, "malformed meta", f.repo.client.Get(t.Context(), d.meta).Val())
}
