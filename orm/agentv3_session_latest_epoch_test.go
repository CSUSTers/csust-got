package orm

import (
	"context"
	"testing"
	"time"

	"csust-got/agent/session"

	"github.com/stretchr/testify/require"
)

func sessionLatestRef(t *testing.T, f *sessionFixture, agent string) (session.NodeRef, error) {
	t.Helper()
	token := sessionRun(t)
	pinned, err := f.repo.ResolveAndPin(t.Context(), session.Selection{Scope: f.scope, Agent: agent, Mode: session.SelectLatest}, token, time.Minute)
	if err != nil {
		return session.NodeRef{}, err
	}
	require.NoError(t, f.repo.Release(t.Context(), f.scope, pinned.Lease))
	return pinned.Nodes[len(pinned.Nodes)-1].Ref, nil
}

func TestAgentV3SessionDropLatestKeepsNewerAndReplies(t *testing.T) {
	f := newSessionFixture(t, session.Options{})
	nodes := map[int]session.Node{}
	for _, c := range []struct {
		agent string
		id    int
	}{{"A", 101}, {"A", 102}, {"A", 103}, {"B", 201}} {
		node, err := f.service.Commit(t.Context(), sessionRequest(t, f.scope, c.agent, nil, c.id, "turn"))
		require.NoError(t, err)
		nodes[c.id] = node
	}

	require.NoError(t, f.service.DropLatest(t.Context(), f.scope, "A", nodes[102].Ref))
	latest, err := sessionLatestRef(t, f, "A")
	require.NoError(t, err)
	require.Equal(t, nodes[103].Ref, latest, "a newer commit stays selectable")

	require.NoError(t, f.service.DropLatest(t.Context(), f.scope, "A", nodes[103].Ref))
	_, err = sessionLatestRef(t, f, "A")
	require.ErrorIs(t, err, session.ErrMiss, "older entries are dropped too, so latest misses")
	latest, err = sessionLatestRef(t, f, "B")
	require.NoError(t, err)
	require.Equal(t, nodes[201].Ref, latest, "other agents keep their latest")
	for _, id := range []int{101, 102, 103} {
		loaded := fixtureLoad(t, f, f.service, id)
		require.Equal(t, nodes[id].Ref, loaded.Parent.Ref(), "reply selection still resolves")
	}

	require.NoError(t, f.service.DropLatest(t.Context(), f.scope, "A", session.NodeRef{DAGID: nodes[101].Ref.DAGID, NodeID: sessionRun(t)}), "an unknown node is a no-op")
	require.ErrorIs(t, f.service.DropLatest(t.Context(), f.scope, "B", nodes[101].Ref), session.ErrCorrupt)
	require.ErrorIs(t, f.service.DropLatest(t.Context(), f.scope, "", nodes[101].Ref), session.ErrCorrupt)

	next, err := f.service.Commit(t.Context(), sessionRequest(t, f.scope, "A", nil, 104, "turn"))
	require.NoError(t, err)
	latest, err = sessionLatestRef(t, f, "A")
	require.NoError(t, err)
	require.Equal(t, next.Ref, latest)
	f.mr.SetTime(f.now.Add(72 * time.Hour))
	require.NoError(t, f.service.Collect(t.Context()), "collection tolerates dropped latest members")
	_, err = sessionLatestRef(t, f, "A")
	require.ErrorIs(t, err, session.ErrMiss)
}

func TestAgentV3SessionMemoryEpochRoundTrip(t *testing.T) {
	f := newSessionFixture(t, session.Options{})
	req := sessionRequest(t, f.scope, "A", nil, 101, "root")
	req.MemoryEpoch = 3
	root, err := f.service.Commit(t.Context(), req)
	require.NoError(t, err)
	require.Equal(t, int64(3), root.MemoryEpoch)

	var seen int64
	loaded, err := f.service.LoadWithAcceptance(t.Context(), session.Selection{Scope: f.scope, Agent: "A", Mode: session.SelectReply, ReplyMessageID: 101}, func(_ context.Context, candidate *session.LoadCandidate) error {
		seen = candidate.MemoryEpoch
		return nil
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = loaded.Parent.Close() })
	require.Equal(t, int64(3), seen)

	child := sessionRequest(t, f.scope, "A", loaded.Parent, 102, "child")
	child.MemoryEpoch = 4
	node, err := f.service.Commit(t.Context(), child)
	require.NoError(t, err)
	require.Equal(t, int64(4), node.MemoryEpoch)
	chain, err := f.repo.Chain(t.Context(), f.scope, node.Ref)
	require.NoError(t, err)
	require.Equal(t, []int64{3, 4}, []int64{chain[0].MemoryEpoch, chain[1].MemoryEpoch})

	compacted, err := f.service.Commit(t.Context(), sessionCompactedRequest(t, f.scope, node))
	require.NoError(t, err)
	require.Equal(t, int64(4), compacted.MemoryEpoch, "a compacted root inherits the source epoch")

	bad := sessionRequest(t, f.scope, "A", nil, 103, "bad")
	bad.MemoryEpoch = -1
	_, err = f.service.Commit(t.Context(), bad)
	require.ErrorIs(t, err, session.ErrCorrupt)
}
