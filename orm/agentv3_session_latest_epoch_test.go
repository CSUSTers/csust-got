package orm

import (
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

func TestAgentV3SessionDropLatestRemovesCompactedRedirect(t *testing.T) {
	f := newSessionFixture(t, session.Options{})
	parent, err := f.service.Commit(t.Context(), sessionRequest(t, f.scope, "A", nil, 101, "turn"))
	require.NoError(t, err)
	compacted, err := f.service.Commit(t.Context(), sessionCompactedRequest(t, f.scope, parent))
	require.NoError(t, err)
	require.Greater(t, compacted.CommitSequence, parent.CommitSequence)
	latest, err := sessionLatestRef(t, f, "A")
	require.NoError(t, err)
	require.Equal(t, compacted.Ref, latest)

	require.NoError(t, f.service.DropLatest(t.Context(), f.scope, "A", parent.Ref))
	_, err = sessionLatestRef(t, f, "A")
	require.ErrorIs(t, err, session.ErrMiss, "a compacted root of the dropped parent must not hide raw turns")

	parent, err = f.service.Commit(t.Context(), sessionRequest(t, f.scope, "A", nil, 102, "turn"))
	require.NoError(t, err)
	_, err = f.service.Commit(t.Context(), sessionCompactedRequest(t, f.scope, parent))
	require.NoError(t, err)
	newer, err := f.service.Commit(t.Context(), sessionRequest(t, f.scope, "A", nil, 103, "turn"))
	require.NoError(t, err)
	require.NoError(t, f.service.DropLatest(t.Context(), f.scope, "A", parent.Ref))
	latest, err = sessionLatestRef(t, f, "A")
	require.NoError(t, err)
	require.Equal(t, newer.Ref, latest, "a genuinely newer commit stays selectable")
}
