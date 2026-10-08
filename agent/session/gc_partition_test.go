package session_test

import (
	"context"
	"io/fs"
	"strconv"
	"testing"
	"time"

	"csust-got/agent/session"

	"github.com/cloudwego/eino/schema"
	"github.com/stretchr/testify/require"
)

func pendingPartitionArchive(t *testing.T, f *acceptanceFixture) session.Intent {
	t.Helper()
	var intent session.Intent
	require.NoError(t, f.files.WithScopeLock(t.Context(), f.scope, func(files *session.ScopeFiles) error {
		var err error
		intent, err = f.repo.Reserve(t.Context(), session.Reservation{Scope: f.scope, Agent: "agent", RunID: acceptanceID(t)}, time.Minute)
		if err != nil {
			return err
		}
		intent.Node.Digest, intent.Node.Size, err = files.WriteAtomic(intent.Node, session.TurnCapture{
			Delta: session.History(schema.UserMessage("healthy pending"), schema.AssistantMessage("pending answer", nil)), Complete: true,
		})
		return err
	}))
	return intent
}

type partitionDAGData struct {
	Meta    string
	Nodes   map[string]string
	Intents map[string]string
	Leases  map[string]string
}

func readPartitionDAG(t *testing.T, f *acceptanceFixture, id string) partitionDAGData {
	t.Helper()
	meta, err := f.client.Get(t.Context(), f.dagKey(id, "meta")).Result()
	require.NoError(t, err)
	nodes, err := f.client.HGetAll(t.Context(), f.dagKey(id, "nodes")).Result()
	require.NoError(t, err)
	intents, err := f.client.HGetAll(t.Context(), f.dagKey(id, "intents")).Result()
	require.NoError(t, err)
	leases, err := f.client.HGetAll(t.Context(), f.dagKey(id, "leases")).Result()
	require.NoError(t, err)
	return partitionDAGData{Meta: meta, Nodes: nodes, Intents: intents, Leases: leases}
}

func TestServiceBadDagAlongsideHealthy(t *testing.T) {
	for _, mode := range []string{"recover", "collect"} {
		for _, part := range []string{"meta", "nodes", "intents", "leases"} {
			t.Run(mode+"/"+part, func(t *testing.T) {
				f := newAcceptanceFixture(t)
				f.redis.SetTime(f.now.Add(-48 * time.Hour))
				bad := f.commit(t, f.scope, nil, 101, "bad DAG file must survive")
				healthy := f.commit(t, f.scope, nil, 102, "healthy expired DAG")
				f.redis.SetTime(f.now)
				if mode == "recover" {
					deletions, err := f.repo.ClaimDeleting(t.Context(), f.scope, 24*time.Hour)
					require.NoError(t, err)
					require.Len(t, deletions, 2)
				}
				pending := pendingPartitionArchive(t, f)
				idle := f.commit(t, f.scope, nil, 103, "idle active DAG")
				idleLastActive := f.dag(t, idle.Ref.DAGID).LastActive
				f.redis.SetTime(f.now.Add(48 * time.Hour))
				const privatePayload = "private malformed DAG value must not enter diagnostics"
				key := f.dagKey(bad.Ref.DAGID, part)
				if part == "meta" {
					require.NoError(t, f.client.Set(t.Context(), key, privatePayload, 0).Err())
				} else {
					field := bad.Ref.NodeID
					if part != "nodes" {
						field = acceptanceID(t)
					}
					require.NoError(t, f.client.HSet(t.Context(), key, field, privatePayload).Err())
				}
				before := readPartitionDAG(t, f, bad.Ref.DAGID)
				messageBefore, err := f.client.HGet(t.Context(), f.scopePrefix()+"messages", strconv.Itoa(101)).Result()
				require.NoError(t, err)
				runBefore, err := f.client.HGet(t.Context(), f.scopePrefix()+"runs", bad.RunID).Result()
				require.NoError(t, err)
				if mode == "recover" {
					err = f.service.Recover(t.Context())
				} else {
					err = f.service.Collect(t.Context())
				}
				require.ErrorIs(t, err, session.ErrCorrupt)
				require.ErrorContains(t, err, bad.Ref.DAGID)
				require.NotContains(t, err.Error(), privatePayload)
				require.Equal(t, before, readPartitionDAG(t, f, bad.Ref.DAGID), "corrupt DAG storage must remain unchanged")
				require.True(t, f.client.SIsMember(t.Context(), f.scopePrefix()+"dags", bad.Ref.DAGID).Val())
				require.Equal(t, messageBefore, f.client.HGet(t.Context(), f.scopePrefix()+"messages", "101").Val())
				require.Equal(t, runBefore, f.client.HGet(t.Context(), f.scopePrefix()+"runs", bad.RunID).Val())
				require.False(t, f.client.SIsMember(t.Context(), f.scopePrefix()+"dags", healthy.Ref.DAGID).Val())
				require.NoError(t, f.files.WithScopeLock(t.Context(), f.scope, func(files *session.ScopeFiles) error {
					_, err := files.Read(bad)
					require.NoError(t, err)
					for _, node := range []session.Node{healthy, pending.Node} {
						_, err = files.Read(node)
						require.ErrorIs(t, err, fs.ErrNotExist, "healthy pending/deleting work must continue")
					}
					return nil
				}))
				if mode == "recover" {
					require.Equal(t, idleLastActive, f.dag(t, idle.Ref.DAGID).LastActive)
					require.True(t, f.client.SIsMember(t.Context(), f.scopePrefix()+"dags", idle.Ref.DAGID).Val())
				} else {
					require.False(t, f.client.SIsMember(t.Context(), f.scopePrefix()+"dags", idle.Ref.DAGID).Val())
				}
			})
		}
	}
}

func TestServicePartialPendingCancellationDoesNotStartRemainingIntents(t *testing.T) {
	f := newAcceptanceFixture(t)
	bad := pendingPartitionArchive(t, f)
	require.NoError(t, f.client.Set(t.Context(), f.dagKey(bad.Node.Ref.DAGID, "meta"), "malformed meta", 0).Err())
	for range 3 {
		pendingPartitionArchive(t, f)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	fault := &cancellationRepo{Repository: f.repo, cancel: cancel}
	files, err := session.NewFileStore(f.dir)
	require.NoError(t, err)
	svc := recoveryService(t, fault, files)
	err = svc.Recover(ctx)
	require.ErrorIs(t, err, context.Canceled)
	require.ErrorIs(t, err, session.ErrCorrupt)
	require.Equal(t, 1, fault.aborts)
	require.Equal(t, 1, fault.finishes)
	pending, err := f.repo.Pending(t.Context(), f.scope)
	require.ErrorIs(t, err, session.ErrCorrupt)
	require.Len(t, pending, 2)
}
