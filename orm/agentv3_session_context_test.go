package orm

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"csust-got/agent/session"

	"github.com/stretchr/testify/require"
)

func TestAgentV3SessionContextRejectionIsCandidateAndModelScoped(t *testing.T) {
	f := newSessionFixture(t, session.Options{TTL: time.Hour})
	older, err := f.service.Commit(t.Context(), sessionRequest(t, f.scope, "A", nil, 101, "older"))
	require.NoError(t, err)
	newer, err := f.service.Commit(t.Context(), sessionRequest(t, f.scope, "A", nil, 102, "newer"))
	require.NoError(t, err)
	archive, err := os.ReadFile(sessionArchivePath(f.dir, newer))
	require.NoError(t, err)
	s, _ := f.repo.scopeKeys(f.scope)
	d := s.dag(newer.Ref.DAGID)
	meta := f.repo.client.Get(t.Context(), d.meta).Val()
	node := f.repo.client.HGet(t.Context(), d.nodes, newer.Ref.NodeID).Val()
	f.mr.SetTime(f.now.Add(30 * time.Minute))
	key := "agent-A/model-X:\x00key"
	require.NoError(t, f.repo.RejectContext(t.Context(), f.scope, newer.Ref, key))
	counter := sessionCountCommands(t, f)
	sessionAssertNoop(t, f, counter, func() {
		require.NoError(t, f.repo.RejectContext(t.Context(), f.scope, newer.Ref, key))
	})
	for _, mode := range []session.SelectionMode{session.SelectLatest, session.SelectReply} {
		sel := session.Selection{Scope: f.scope, Agent: "A", Mode: mode, ReplyMessageID: 102, ContextKey: key}
		pin, err := f.repo.ResolveAndPin(t.Context(), sel, sessionRun(t), time.Hour)
		require.ErrorIs(t, err, session.ErrContextRejected)
		require.Equal(t, session.Pinned{}, pin, "latest must not silently select the unrelated older candidate")
		for _, otherKey := range []string{"", "agent-A/model-Y", "agent-B/model-X"} {
			sel.ContextKey = otherKey
			pin, err := f.repo.ResolveAndPin(t.Context(), sel, sessionRun(t), time.Hour)
			require.NoError(t, err)
			require.Equal(t, newer.Ref, pin.Nodes[0].Ref)
			require.NoError(t, f.repo.Release(t.Context(), f.scope, pin.Lease))
		}
	}
	oldPin, err := f.repo.ResolveAndPin(t.Context(), session.Selection{Scope: f.scope, Mode: session.SelectReply, ReplyMessageID: 101, ContextKey: key}, sessionRun(t), time.Hour)
	require.NoError(t, err)
	require.Equal(t, older.Ref, oldPin.Nodes[0].Ref)
	require.NoError(t, f.repo.Release(t.Context(), f.scope, oldPin.Lease))
	require.Equal(t, meta, f.repo.client.Get(t.Context(), d.meta).Val(), "rejection and selection alone never touch LastActive")
	require.Equal(t, node, f.repo.client.HGet(t.Context(), d.nodes, newer.Ref.NodeID).Val())
	after, err := os.ReadFile(sessionArchivePath(f.dir, newer))
	require.NoError(t, err)
	require.Equal(t, archive, after)
	f.mr.SetTime(f.now.Add(2 * time.Hour))
	require.NoError(t, f.service.Collect(t.Context()))
	require.Equal(t, sessionRedisNone, f.repo.client.Type(t.Context(), d.rejectedContexts).Val(), "owned rejection key is removed with the manifest")
}

func TestAgentV3SessionRejectContextRequiresPublishedMatchingActiveOwnership(t *testing.T) {
	for _, fault := range []string{"unpublished", "missing", "scope", "namespace", "deleting", "member", "run", "empty-key", "invalid-ref"} {
		t.Run(fault, func(t *testing.T) {
			f := newSessionFixture(t, session.Options{})
			i, err := f.repo.Reserve(t.Context(), session.Reservation{Scope: f.scope, Agent: "A", RunID: sessionRun(t)}, time.Minute)
			require.NoError(t, err)
			ref := i.Node.Ref
			if fault != "unpublished" {
				_, err = f.repo.Publish(t.Context(), f.scope, i, fmt.Sprintf("%064x", 1), 10, session.DeliveryReceipt{MessageIDs: []int{101}})
				require.NoError(t, err)
				require.NoError(t, f.repo.FinishIntent(t.Context(), f.scope, i))
			}
			s, _ := f.repo.scopeKeys(f.scope)
			d := s.dag(ref.DAGID)
			scope, key := f.scope, "agent/model"
			switch fault {
			case "missing":
				ref.NodeID = sessionRun(t)
			case "scope":
				scope.ChatID++
			case "namespace":
				scope.Namespace = fmt.Sprintf("%064x", 1)
			case "deleting":
				f.mr.SetTime(f.now.Add(2 * time.Hour))
				_, err := f.repo.ClaimDeleting(t.Context(), f.scope, time.Hour)
				require.NoError(t, err)
			case "member":
				require.NoError(t, f.repo.client.SRem(t.Context(), s.dags, d.id).Err())
			case "run":
				require.NoError(t, f.repo.client.HDel(t.Context(), s.runs, i.Node.RunID).Err())
			case "empty-key":
				key = ""
			case "invalid-ref":
				ref.NodeID = "invalid"
			}
			counter := sessionCountCommands(t, f)
			require.Error(t, f.repo.RejectContext(t.Context(), scope, ref, key))
			require.Zero(t, counter.snapshot().commands["hset"])
			require.Equal(t, sessionRedisNone, f.repo.client.Type(t.Context(), d.rejectedContexts).Val())
		})
	}
}

func TestAgentV3SessionPinRetriesConcurrentContextRejection(t *testing.T) {
	for _, mode := range []session.SelectionMode{session.SelectReply, session.SelectLatest} {
		t.Run(string(mode), func(t *testing.T) {
			f := newSessionFixture(t, session.Options{})
			_, root := sessionRootIntent(t, f, 101)
			other := sessionOtherRepository(t, f)
			counter := sessionCountCommands(t, f)
			f.repo.client.AddHook(&sessionBeforeExecHook{run: func(ctx context.Context) error {
				return other.RejectContext(ctx, f.scope, root.Ref, "agent/model")
			}})
			pin, err := f.repo.ResolveAndPin(t.Context(), session.Selection{Scope: f.scope, Agent: "A", Mode: mode, ReplyMessageID: 101, ContextKey: "agent/model"}, sessionRun(t), time.Hour)
			require.ErrorIs(t, err, session.ErrContextRejected)
			require.Equal(t, session.Pinned{}, pin)
			require.EqualValues(t, 1, counter.snapshot().conflicts)
			require.Empty(t, sessionReadState(t, f).DAGs[root.Ref.DAGID].Leases, "failed selection cannot publish a pin")
		})
	}
}

func TestAgentV3SessionRejectContextRetriesDeletingTransition(t *testing.T) {
	f := newSessionFixture(t, session.Options{})
	_, root := sessionRootIntent(t, f, 101)
	f.mr.SetTime(f.now.Add(2 * time.Hour))
	other := sessionOtherRepository(t, f)
	counter := sessionCountCommands(t, f)
	f.repo.client.AddHook(&sessionBeforeExecHook{run: func(ctx context.Context) error {
		_, err := other.ClaimDeleting(ctx, f.scope, time.Hour)
		return err
	}})
	require.ErrorIs(t, f.repo.RejectContext(t.Context(), f.scope, root.Ref, "agent/model"), session.ErrFence)
	require.EqualValues(t, 1, counter.snapshot().conflicts)
	s, _ := f.repo.scopeKeys(f.scope)
	require.Equal(t, sessionRedisNone, f.repo.client.Type(t.Context(), s.dag(root.Ref.DAGID).rejectedContexts).Val())
}

func TestAgentV3SessionRejectedContextCorruptionIsRetained(t *testing.T) {
	for _, stage := range []string{"claim", "finish"} {
		for _, fault := range []string{"type", "field", "unknown-node", "value"} {
			t.Run(stage+"/"+fault, func(t *testing.T) {
				f := newSessionFixture(t, session.Options{})
				_, root := sessionRootIntent(t, f, 101)
				require.NoError(t, f.repo.RejectContext(t.Context(), f.scope, root.Ref, "agent/model"))
				f.mr.SetTime(f.now.Add(2 * time.Hour))
				var deletion session.Deletion
				if stage == "finish" {
					deletions, err := f.repo.ClaimDeleting(t.Context(), f.scope, time.Hour)
					require.NoError(t, err)
					require.Len(t, deletions, 1)
					deletion = deletions[0]
				}
				s, _ := f.repo.scopeKeys(f.scope)
				d := s.dag(root.Ref.DAGID)
				switch fault {
				case "type":
					require.NoError(t, f.repo.client.Del(t.Context(), d.rejectedContexts).Err())
					require.NoError(t, f.repo.client.Set(t.Context(), d.rejectedContexts, "private data", 0).Err())
				case "field":
					require.NoError(t, f.repo.client.HSet(t.Context(), d.rejectedContexts, "invalid", "1").Err())
				case "unknown-node":
					require.NoError(t, f.repo.client.HSet(t.Context(), d.rejectedContexts, sessionContextField("agent/model", sessionRun(t)), "1").Err())
				case "value":
					require.NoError(t, f.repo.client.HSet(t.Context(), d.rejectedContexts, sessionContextField("agent/model", root.Ref.NodeID), "private value").Err())
				}
				counter := sessionCountCommands(t, f)
				var err error
				if stage == "claim" {
					deletions, claimErr := f.repo.ClaimDeleting(t.Context(), f.scope, time.Hour)
					err = claimErr
					require.Empty(t, deletions)
				} else {
					err = f.repo.FinishDelete(t.Context(), f.scope, deletion)
				}
				require.ErrorIs(t, err, session.ErrCorrupt)
				require.NotContains(t, err.Error(), "private")
				require.Zero(t, counter.snapshot().commands["del"])
				require.True(t, f.repo.client.SIsMember(t.Context(), s.dags, d.id).Val())
				require.NotEqual(t, sessionRedisNone, f.repo.client.Type(t.Context(), d.rejectedContexts).Val())
			})
		}
	}
}

func TestAgentV3SessionContextKeyEmptyBypassesRejectionStorage(t *testing.T) {
	f := newSessionFixture(t, session.Options{})
	_, root := sessionRootIntent(t, f, 101)
	s, _ := f.repo.scopeKeys(f.scope)
	d := s.dag(root.Ref.DAGID)
	require.NoError(t, f.repo.client.Set(t.Context(), d.rejectedContexts, "private wrong type", 0).Err())
	for _, contextKey := range []string{"", "agent/model"} {
		pin, err := f.repo.ResolveAndPin(t.Context(), session.Selection{Scope: f.scope, Mode: session.SelectReply, ReplyMessageID: 101, ContextKey: contextKey, LoadOnly: true}, sessionRun(t), time.Hour)
		if contextKey == "" {
			require.NoError(t, err)
			require.Equal(t, root.Ref, pin.Nodes[0].Ref)
			require.NoError(t, f.repo.Release(t.Context(), f.scope, pin.Lease))
		} else {
			require.ErrorIs(t, err, session.ErrCorrupt)
			require.Equal(t, session.Pinned{}, pin)
		}
	}
}

func TestAgentV3SessionFinalDeleteRechecksRejectionManifest(t *testing.T) {
	f := newSessionFixture(t, session.Options{})
	_, root := sessionRootIntent(t, f, 101)
	require.NoError(t, f.repo.RejectContext(t.Context(), f.scope, root.Ref, "agent/model"))
	f.mr.SetTime(f.now.Add(2 * time.Hour))
	deletions, err := f.repo.ClaimDeleting(t.Context(), f.scope, time.Hour)
	require.NoError(t, err)
	other := sessionOtherRepository(t, f)
	s, _ := f.repo.scopeKeys(f.scope)
	d := s.dag(root.Ref.DAGID)
	counter := sessionCountCommands(t, f)
	f.repo.client.AddHook(&sessionBeforeExecHook{run: func(ctx context.Context) error {
		return other.client.HSet(ctx, d.rejectedContexts, sessionContextField("unknown/context", sessionRun(t)), "1").Err()
	}})
	require.ErrorIs(t, f.repo.FinishDelete(t.Context(), f.scope, deletions[0]), session.ErrCorrupt)
	require.EqualValues(t, 1, counter.snapshot().conflicts)
	require.True(t, f.repo.client.SIsMember(t.Context(), s.dags, root.Ref.DAGID).Val())
}
