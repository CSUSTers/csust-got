package orm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"csust-got/agent/session"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func sessionOtherRepository(t *testing.T, f *sessionFixture) *AgentV3SessionRepository {
	t.Helper()
	client := redis.NewClient(&redis.Options{Addr: f.mr.Addr(), MaxRetries: -1})
	f.mr.configureClient(client)
	t.Cleanup(func() { _ = client.Close() })
	r, err := NewAgentV3SessionRepository(client, "session-test:")
	require.NoError(t, err)
	return r
}

func sessionRootIntent(t *testing.T, f *sessionFixture, id int) (session.Intent, session.Node) {
	t.Helper()
	i, err := f.repo.Reserve(t.Context(), session.Reservation{Scope: f.scope, Agent: "A", RunID: sessionRun(t)}, time.Hour)
	require.NoError(t, err)
	n, err := f.repo.Publish(t.Context(), f.scope, i, strings.Repeat("a", 64), 100, session.DeliveryReceipt{MessageIDs: []int{id}})
	require.NoError(t, err)
	require.NoError(t, f.repo.FinishIntent(t.Context(), f.scope, i))
	return i, n
}

func TestAgentV3SessionPartitionKeysAndOldDataUntouched(t *testing.T) {
	f := newSessionFixture(t, session.Options{TTL: time.Hour})
	oldKey := f.repo.base + "scope:" + f.scope.Key()
	require.NoError(t, f.repo.client.Set(t.Context(), oldKey, "unknown old payload", 0).Err())
	root, err := f.service.Commit(t.Context(), sessionRequest(t, f.scope, "A", nil, 101, "root"))
	require.NoError(t, err)
	s, err := f.repo.scopeKeys(f.scope)
	require.NoError(t, err)
	d := s.dag(root.Ref.DAGID)
	var meta map[string]json.RawMessage
	require.NoError(t, json.Unmarshal([]byte(f.repo.client.Get(t.Context(), d.meta).Val()), &meta))
	require.Len(t, meta, 6)
	require.JSONEq(t, "2", string(meta["layout"]))
	require.NotContains(t, meta, "nodes")
	require.Equal(t, "hash", f.repo.client.Type(t.Context(), d.nodes).Val())
	require.Equal(t, "set", f.repo.client.Type(t.Context(), s.dags).Val())
	require.Equal(t, "zset", f.repo.client.Type(t.Context(), s.latest("A")).Val())
	loaded := fixtureLoad(t, f, f.service, 101)
	require.Equal(t, root.Ref, loaded.Parent.Ref())
	require.NoError(t, loaded.Parent.Close())
	f.mr.SetTime(f.now.Add(2 * time.Hour))
	require.NoError(t, f.service.Collect(t.Context()))
	require.Equal(t, "unknown old payload", f.repo.client.Get(t.Context(), oldKey).Val())
	for _, key := range []string{d.meta, d.nodes, d.leases, d.intents, s.dags, s.runs, s.messages, s.sequence, s.latest("A")} {
		require.Equal(t, "none", f.repo.client.Type(t.Context(), key).Val(), key)
	}
}

func TestAgentV3SessionCatalogOwnershipAndConcurrentRewrite(t *testing.T) {
	for _, stage := range []string{"root", "final"} {
		for _, bad := range []string{"malformed", "misbound", "namespace", "type", "rewrite"} {
			t.Run(stage+"/"+bad, func(t *testing.T) {
				f := newSessionFixture(t, session.Options{})
				s, _ := f.repo.scopeKeys(f.scope)
				catalog := f.repo.base + "scopes"
				var deletion session.Deletion
				if stage == "final" {
					_, _ = sessionRootIntent(t, f, 101)
					f.mr.SetTime(f.now.Add(2 * time.Hour))
					claimed, err := f.repo.ClaimDeleting(t.Context(), f.scope, time.Hour)
					require.NoError(t, err)
					require.Len(t, claimed, 1)
					deletion = claimed[0]
				}
				value := `{"private":"catalog secret"`
				if bad == "misbound" {
					other := f.scope
					other.ChatID++
					value = sessionEncode(other)
				}
				if bad == "namespace" {
					other := f.scope
					other.Namespace = strings.Repeat("f", 64)
					value = sessionEncode(other)
				}
				corrupt := func(client *redis.Client) error {
					if bad == "type" {
						if err := client.Del(t.Context(), catalog).Err(); err != nil {
							return err
						}
						return client.Set(t.Context(), catalog, value, 0).Err()
					}
					return client.HSet(t.Context(), catalog, f.scope.Key(), value).Err()
				}
				if bad == "rewrite" {
					other := sessionOtherRepository(t, f)
					f.repo.client.AddHook(&sessionBeforeExecHook{run: func(context.Context) error { return corrupt(other.client) }})
				} else {
					require.NoError(t, corrupt(f.repo.client))
				}
				var err error
				if stage == "root" {
					_, err = f.repo.Reserve(t.Context(), session.Reservation{Scope: f.scope, Agent: "A", RunID: sessionRun(t)}, time.Hour)
				} else {
					err = f.repo.FinishDelete(t.Context(), f.scope, deletion)
				}
				require.ErrorIs(t, err, session.ErrCorrupt)
				if bad == "type" {
					require.Equal(t, value, f.repo.client.Get(t.Context(), catalog).Val())
				} else {
					require.Equal(t, value, f.repo.client.HGet(t.Context(), catalog, f.scope.Key()).Val())
				}
				if stage == "root" {
					require.Zero(t, f.repo.client.SCard(t.Context(), s.dags).Val())
					require.Equal(t, "none", f.repo.client.Type(t.Context(), s.runs).Val())
				} else {
					require.True(t, f.repo.client.SIsMember(t.Context(), s.dags, deletion.DAGID).Val())
					require.Equal(t, sessionDAGDeleting, sessionReadState(t, f).DAGs[deletion.DAGID].State)
				}
			})
		}
	}
}

func TestAgentV3SessionFinalDeleteCannotDeleteNewRootCatalog(t *testing.T) {
	f := newSessionFixture(t, session.Options{})
	_, old := sessionRootIntent(t, f, 101)
	f.mr.SetTime(f.now.Add(2 * time.Hour))
	deletions, err := f.repo.ClaimDeleting(t.Context(), f.scope, time.Hour)
	require.NoError(t, err)
	require.Len(t, deletions, 1)
	other := sessionOtherRepository(t, f)
	var newIntent session.Intent
	counts := sessionCountCommands(t, f)
	f.repo.client.AddHook(&sessionBeforeExecHook{run: func(ctx context.Context) error {
		var err error
		newIntent, err = other.Reserve(ctx, session.Reservation{Scope: f.scope, Agent: "B", RunID: sessionRun(t)}, time.Hour)
		return err
	}})
	require.NoError(t, f.repo.FinishDelete(t.Context(), f.scope, deletions[0]))
	require.EqualValues(t, 1, counts.snapshot().conflicts)
	s, _ := f.repo.scopeKeys(f.scope)
	require.Equal(t, []string{newIntent.Node.Ref.DAGID}, f.repo.client.SMembers(t.Context(), s.dags).Val())
	require.Equal(t, sessionEncode(f.scope), f.repo.client.HGet(t.Context(), f.repo.base+"scopes", f.scope.Key()).Val())
	pending, err := f.repo.Pending(t.Context(), f.scope)
	require.NoError(t, err)
	require.Equal(t, []session.Intent{newIntent}, pending)
	require.NotContains(t, sessionReadState(t, f).Runs, old.RunID)
	require.Equal(t, "1", f.repo.client.Get(t.Context(), s.sequence).Val())
}

func TestAgentV3SessionFinalDeletePreservesUnknownReferences(t *testing.T) {
	f := newSessionFixture(t, session.Options{})
	_, _ = sessionRootIntent(t, f, 101)
	f.mr.SetTime(f.now.Add(2 * time.Hour))
	deletions, err := f.repo.ClaimDeleting(t.Context(), f.scope, time.Hour)
	require.NoError(t, err)
	s, _ := f.repo.scopeKeys(f.scope)
	require.NoError(t, f.repo.client.HSet(t.Context(), s.messages, "999", sessionEncode(session.NodeRef{DAGID: sessionRun(t), NodeID: sessionRun(t)})).Err())
	require.ErrorIs(t, f.repo.FinishDelete(t.Context(), f.scope, deletions[0]), session.ErrCorrupt)
	require.NotEmpty(t, f.repo.client.HGet(t.Context(), f.repo.base+"scopes", f.scope.Key()).Val())
	require.True(t, f.repo.client.SIsMember(t.Context(), s.dags, deletions[0].DAGID).Val())
}

func TestAgentV3SessionLatestExactSequenceAndOlderCandidate(t *testing.T) {
	f := newSessionFixture(t, session.Options{})
	_, _ = sessionRootIntent(t, f, 101)
	s, _ := f.repo.scopeKeys(f.scope)
	require.NoError(t, f.repo.client.Set(t.Context(), s.sequence, strconv.FormatInt(1<<53-1, 10), 0).Err())
	_, older := sessionRootIntent(t, f, 102)
	_, newer := sessionRootIntent(t, f, 103)
	require.EqualValues(t, 1<<53, older.CommitSequence)
	require.EqualValues(t, 1<<53+1, newer.CommitSequence)
	pin, err := f.repo.ResolveAndPin(t.Context(), session.Selection{Scope: f.scope, Agent: "A", Mode: session.SelectLatest}, sessionRun(t), time.Hour)
	require.NoError(t, err)
	require.Equal(t, newer.Ref, pin.Nodes[len(pin.Nodes)-1].Ref)
	require.NoError(t, f.repo.Release(t.Context(), f.scope, pin.Lease))
	f.mr.SetTime(f.now.Add(2 * time.Hour))
	pin, err = f.repo.ResolveAndPin(t.Context(), session.Selection{Scope: f.scope, Mode: session.SelectReply, ReplyMessageID: 102}, sessionRun(t), time.Hour)
	require.NoError(t, err)
	require.NoError(t, f.repo.ConfirmLoaded(t.Context(), f.scope, pin.Lease, time.Hour))
	require.NoError(t, f.repo.Release(t.Context(), f.scope, pin.Lease))
	deletions, err := f.repo.ClaimDeleting(t.Context(), f.scope, time.Hour)
	require.NoError(t, err)
	require.Len(t, deletions, 2)
	for _, deletion := range deletions {
		require.NoError(t, f.repo.FinishDelete(t.Context(), f.scope, deletion))
	}
	pin, err = f.repo.ResolveAndPin(t.Context(), session.Selection{Scope: f.scope, Agent: "A", Mode: session.SelectLatest}, sessionRun(t), time.Hour)
	require.NoError(t, err)
	require.Equal(t, older.Ref, pin.Nodes[0].Ref)
	require.NoError(t, f.repo.Release(t.Context(), f.scope, pin.Lease))
	i, err := f.repo.Reserve(t.Context(), session.Reservation{Scope: f.scope, Agent: "A", RunID: sessionRun(t)}, time.Hour)
	require.NoError(t, err)
	require.NoError(t, f.repo.client.Set(t.Context(), s.sequence, strconv.FormatInt(math.MaxInt64, 10), 0).Err())
	_, err = f.repo.Publish(t.Context(), f.scope, i, strings.Repeat("a", 64), 100, session.DeliveryReceipt{MessageIDs: []int{104}})
	require.ErrorIs(t, err, session.ErrConflict)
	require.Empty(t, f.repo.client.HGetAll(t.Context(), s.dag(i.Node.Ref.DAGID).nodes).Val())
	require.Equal(t, strconv.FormatInt(math.MaxInt64, 10), f.repo.client.Get(t.Context(), s.sequence).Val())
}

func TestAgentV3SessionPublicationSnapshotRetriesConcurrentPublish(t *testing.T) {
	f := newSessionFixture(t, session.Options{})
	other := sessionOtherRepository(t, f)
	run := sessionRun(t)
	counts := sessionCountCommands(t, f)
	var published session.Node
	f.repo.client.AddHook(&sessionBeforeExecHook{run: func(ctx context.Context) error {
		i, err := other.Reserve(ctx, session.Reservation{Scope: f.scope, Agent: "A", RunID: run}, time.Hour)
		if err != nil {
			return err
		}
		published, err = other.Publish(ctx, f.scope, i, strings.Repeat("a", 64), 100, session.DeliveryReceipt{MessageIDs: []int{101}})
		return err
	}})
	actual, err := f.repo.GetPublication(t.Context(), f.scope, run)
	require.NoError(t, err)
	require.NotNil(t, actual)
	require.Equal(t, published, *actual)
	require.EqualValues(t, 1, counts.snapshot().conflicts)
}

func TestAgentV3SessionIndependentDAGRenewAndSameDAGConflict(t *testing.T) {
	for _, same := range []bool{false, true} {
		t.Run(strconv.FormatBool(same), func(t *testing.T) {
			f := newSessionFixture(t, session.Options{})
			sessionSeedBenchmark(t, f, 2)
			pins := make([]session.Pinned, 2)
			for i := range pins {
				id := 101 + i
				if same {
					id = 101
				}
				var err error
				pins[i], err = f.repo.ResolveAndPin(t.Context(), session.Selection{Scope: f.scope, Mode: session.SelectReply, ReplyMessageID: id}, sessionRun(t), time.Hour)
				require.NoError(t, err)
			}
			before := sessionReadState(t, f)
			f.mr.SetTime(f.now.Add(time.Millisecond))
			other := sessionOtherRepository(t, f)
			counts := sessionCountCommands(t, f)
			f.repo.client.AddHook(&sessionBeforeExecHook{run: func(ctx context.Context) error { return other.Renew(ctx, f.scope, pins[1].Lease, time.Hour) }})
			require.NoError(t, f.repo.Renew(t.Context(), f.scope, pins[0].Lease, time.Hour))
			want := int64(0)
			if same {
				want = 1
			}
			require.Equal(t, want, counts.snapshot().conflicts)
			after := sessionReadState(t, f)
			for _, pin := range pins {
				require.Greater(t, after.DAGs[pin.Lease.DAGID].Leases[pin.Lease.Token].Deadline, pin.Lease.Deadline)
			}
			for id, d := range before.DAGs {
				require.Equal(t, d.Nodes, after.DAGs[id].Nodes)
				require.Equal(t, d.LastActive, after.DAGs[id].LastActive)
			}
		})
	}
}

func TestAgentV3SessionPublishWrongTypesNeverPartiallyApplies(t *testing.T) {
	for _, family := range []string{"nodes", "intents", "leases", "sequence", "messages", "latest", "runs"} {
		t.Run(family, func(t *testing.T) {
			f := newSessionFixture(t, session.Options{})
			i, err := f.repo.Reserve(t.Context(), session.Reservation{Scope: f.scope, Agent: "A", RunID: sessionRun(t)}, time.Hour)
			require.NoError(t, err)
			s, _ := f.repo.scopeKeys(f.scope)
			d := s.dag(i.Node.Ref.DAGID)
			key := map[string]string{"nodes": d.nodes, "intents": d.intents, "leases": d.leases, "sequence": s.sequence, "messages": s.messages, "latest": s.latest("A"), "runs": s.runs}[family]
			require.NoError(t, f.repo.client.Del(t.Context(), key).Err())
			if family == "sequence" {
				require.NoError(t, f.repo.client.HSet(t.Context(), key, "unknown", "private metadata").Err())
			} else {
				require.NoError(t, f.repo.client.Set(t.Context(), key, "private metadata", 0).Err())
			}
			counts := sessionCountCommands(t, f)
			out, err := f.repo.Publish(t.Context(), f.scope, i, strings.Repeat("a", 64), 100, session.DeliveryReceipt{MessageIDs: []int{101}})
			require.ErrorIs(t, err, session.ErrCorrupt)
			require.Equal(t, session.Node{}, out)
			require.NotContains(t, err.Error(), "private metadata")
			for _, name := range []string{"hset", "set", "zadd", "del", "hdel"} {
				require.Zero(t, counts.snapshot().commands[name])
			}
			if family != "runs" {
				var run sessionRunIndex
				require.NoError(t, json.Unmarshal([]byte(f.repo.client.HGet(t.Context(), s.runs, i.Node.RunID).Val()), &run))
				require.Equal(t, sessionIntentPending, run.Status)
			}
		})
	}
}

func TestAgentV3SessionPartialCorruptDAGsRetainFilesAndAllowHealthyGC(t *testing.T) {
	f := newSessionFixture(t, session.Options{TTL: time.Hour})
	badNodes := make([]session.Node, 4)
	for i := range badNodes {
		var err error
		badNodes[i], err = f.service.Commit(t.Context(), sessionRequest(t, f.scope, "A", nil, 101+i, "retained"))
		require.NoError(t, err)
	}
	healthy, err := f.service.Commit(t.Context(), sessionRequest(t, f.scope, "A", nil, 201, "healthy"))
	require.NoError(t, err)
	pending, err := f.repo.Reserve(t.Context(), session.Reservation{Scope: f.scope, Agent: "A", RunID: sessionRun(t)}, time.Hour)
	require.NoError(t, err)
	s, _ := f.repo.scopeKeys(f.scope)
	require.NoError(t, f.repo.client.Set(t.Context(), s.dag(badNodes[0].Ref.DAGID).meta, `{"private":"corrupt meta"`, 0).Err())
	require.NoError(t, f.repo.client.HSet(t.Context(), s.dag(badNodes[1].Ref.DAGID).nodes, badNodes[1].Ref.NodeID, `{"private":"corrupt node"}`).Err())
	require.NoError(t, f.repo.client.HSet(t.Context(), s.dag(badNodes[2].Ref.DAGID).leases, sessionRun(t), `{"private":"corrupt lease"}`).Err())
	badIntent := pending
	badIntent.Node.Ref = badNodes[3].Ref
	badIntent.Lease.DAGID = badNodes[3].Ref.DAGID
	require.NoError(t, f.repo.client.HSet(t.Context(), s.dag(badNodes[3].Ref.DAGID).intents, badIntent.Node.RunID, sessionEncode(badIntent)).Err())
	require.NoError(t, f.repo.client.SAdd(t.Context(), s.pending, badNodes[0].Ref.DAGID, badNodes[3].Ref.DAGID).Err())
	intents, err := f.repo.Pending(t.Context(), f.scope)
	require.ErrorIs(t, err, session.ErrCorrupt)
	require.Contains(t, intents, pending)
	require.NotContains(t, err.Error(), "private")
	f.mr.SetTime(f.now.Add(2 * time.Hour))
	require.ErrorIs(t, f.service.Collect(t.Context()), session.ErrCorrupt)
	for _, node := range badNodes {
		_, err := os.Stat(sessionArchivePath(f.dir, node))
		require.NoError(t, err)
		require.True(t, f.repo.client.SIsMember(t.Context(), s.dags, node.Ref.DAGID).Val())
	}
	_, err = os.Stat(sessionArchivePath(f.dir, healthy))
	require.ErrorIs(t, err, os.ErrNotExist)
	require.False(t, f.repo.client.SIsMember(t.Context(), s.dags, healthy.Ref.DAGID).Val())
}

type sessionFailReadHook struct {
	failure error
	seen    map[string]bool
}

func (h *sessionFailReadHook) fail(cmd redis.Cmder) bool {
	if cmd.Name() != "hgetall" {
		return false
	}
	if h.seen == nil {
		return true
	}
	key := fmt.Sprint(cmd.Args()[1])
	if !strings.HasSuffix(key, ":leases") {
		return false
	}
	h.seen[key] = true
	return len(h.seen) > 1
}

func (*sessionFailReadHook) DialHook(next redis.DialHook) redis.DialHook { return next }
func (h *sessionFailReadHook) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error {
		if h.fail(cmd) {
			return h.failure
		}
		return next(ctx, cmd)
	}
}
func (h *sessionFailReadHook) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return func(ctx context.Context, cmds []redis.Cmder) error {
		for _, cmd := range cmds {
			if h.fail(cmd) {
				cmd.SetErr(h.failure)
				return h.failure
			}
		}
		return next(ctx, cmds)
	}
}

func TestAgentV3SessionMixedTransportErrorNeverReturnsPartial(t *testing.T) {
	f := newSessionFixture(t, session.Options{})
	sessionRootIntent(t, f, 101)
	sessionRootIntent(t, f, 102)
	f.mr.SetTime(f.now.Add(2 * time.Hour))
	failure := errors.Join(session.ErrCorrupt, errSessionDisconnected)
	f.repo.client.AddHook(&sessionFailReadHook{failure: failure, seen: map[string]bool{}})
	deletions, err := f.repo.ClaimDeleting(t.Context(), f.scope, time.Hour)
	require.ErrorIs(t, err, errSessionDisconnected)
	require.Nil(t, deletions)
}

func TestAgentV3SessionHotRenewTouchesOnlyTargetMetaAndLeaseField(t *testing.T) {
	f := newSessionFixture(t, session.Options{})
	sessionSeedBenchmark(t, f, 100)
	pin, err := f.repo.ResolveAndPin(t.Context(), session.Selection{Scope: f.scope, Mode: session.SelectReply, ReplyMessageID: 101}, sessionRun(t), time.Hour)
	require.NoError(t, err)
	s, _ := f.repo.scopeKeys(f.scope)
	d := s.dag(pin.Lease.DAGID)
	counter := sessionCountCommands(t, f)
	counter.trace = true
	f.mr.SetTime(f.now.Add(time.Millisecond))
	require.NoError(t, f.repo.Renew(t.Context(), f.scope, pin.Lease, time.Hour))
	for _, event := range counter.events {
		require.Contains(t, []string{d.meta, d.leases}, event.key)
		if event.name == "hget" || event.name == "hset" {
			require.Equal(t, pin.Lease.Token, event.field)
		}
	}
	stats := counter.snapshot()
	require.EqualValues(t, 1, stats.commands["hset"])
	require.Zero(t, stats.commands["hgetall"])
	require.Zero(t, stats.commands["smembers"])
	require.Equal(t, int64(len(sessionEncode(pin.Lease))), stats.writeValues["leases"])
}

func TestAgentV3SessionAbortCannotCompensateInconsistentPublishedNode(t *testing.T) {
	f := newSessionFixture(t, session.Options{})
	i, err := f.repo.Reserve(t.Context(), session.Reservation{Scope: f.scope, Agent: "A", RunID: sessionRun(t)}, time.Hour)
	require.NoError(t, err)
	s, _ := f.repo.scopeKeys(f.scope)
	n := i.Node
	n.Digest = strings.Repeat("a", 64)
	n.Size = 100
	n.CommitSequence = 1
	n.ReplyMessageIDs = []int{101}
	require.NoError(t, f.repo.client.HSet(t.Context(), s.dag(n.Ref.DAGID).nodes, n.Ref.NodeID, sessionEncode(n)).Err())
	aborted, err := f.repo.AbortIntent(t.Context(), f.scope, i)
	require.ErrorIs(t, err, session.ErrCorrupt)
	require.False(t, aborted)
	_, err = f.repo.GetPublication(t.Context(), f.scope, n.RunID)
	require.ErrorIs(t, err, session.ErrCorrupt)
}

func TestAgentV3SessionPinRetriesChangedReplySelection(t *testing.T) {
	f := newSessionFixture(t, session.Options{})
	_, old := sessionRootIntent(t, f, 101)
	other := sessionOtherRepository(t, f)
	i, err := other.Reserve(t.Context(), session.Reservation{Scope: f.scope, Agent: "B", RunID: sessionRun(t)}, time.Hour)
	require.NoError(t, err)
	counts := sessionCountCommands(t, f)
	f.repo.client.AddHook(&sessionBeforeExecHook{run: func(ctx context.Context) error {
		_, err := other.Publish(ctx, f.scope, i, strings.Repeat("b", 64), 100, session.DeliveryReceipt{MessageIDs: []int{101}})
		return err
	}})
	pin, err := f.repo.ResolveAndPin(t.Context(), session.Selection{Scope: f.scope, Mode: session.SelectReply, ReplyMessageID: 101}, sessionRun(t), time.Hour)
	require.NoError(t, err)
	require.Equal(t, i.Node.Ref, pin.Nodes[0].Ref)
	require.EqualValues(t, 1, counts.snapshot().conflicts)
	require.Empty(t, sessionReadState(t, f).DAGs[old.Ref.DAGID].Leases, "the failed selection's token must never be published")
}

func TestAgentV3SessionCorruptDeletingDoesNotHideHealthyTombstone(t *testing.T) {
	f := newSessionFixture(t, session.Options{})
	_, a := sessionRootIntent(t, f, 101)
	_, b := sessionRootIntent(t, f, 102)
	f.mr.SetTime(f.now.Add(2 * time.Hour))
	claimed, err := f.repo.ClaimDeleting(t.Context(), f.scope, time.Hour)
	require.NoError(t, err)
	require.Len(t, claimed, 2)
	s, _ := f.repo.scopeKeys(f.scope)
	require.NoError(t, f.repo.client.HSet(t.Context(), s.dag(a.Ref.DAGID).nodes, a.Ref.NodeID, "corrupt private tombstone").Err())
	deletions, err := f.repo.Deleting(t.Context(), f.scope)
	require.ErrorIs(t, err, session.ErrCorrupt)
	require.True(t, sessionPureCorruption(err))
	require.Len(t, deletions, 1)
	require.Equal(t, b.Ref.DAGID, deletions[0].DAGID)
	require.NotContains(t, err.Error(), "private tombstone")
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	deletions, err = f.repo.Deleting(ctx, f.scope)
	require.ErrorIs(t, err, context.Canceled)
	require.Nil(t, deletions)
}
