package orm

import (
	"encoding/json"
	"errors"
	"strconv"
	"testing"

	"csust-got/agent/session"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

// Aggregate views are test-only; production operations never rebuild this graph.
type sessionDAG struct {
	ID         string
	Generation string
	State      string
	LastActive int64
	Nodes      map[string]session.Node
	Intents    map[string]session.Intent
	Leases     map[string]session.Lease
}
type sessionState struct {
	Version  int
	Scope    session.Scope
	Sequence int64
	DAGs     map[string]*sessionDAG
	Messages map[string]session.NodeRef
	Runs     map[string]session.NodeRef
}

func emptySessionState(scope session.Scope) sessionState {
	return sessionState{Version: session.Version, Scope: scope, DAGs: map[string]*sessionDAG{}, Messages: map[string]session.NodeRef{}, Runs: map[string]session.NodeRef{}}
}

func sessionFixtureState(t testing.TB, f *sessionFixture) sessionState {
	t.Helper()
	s, err := f.repo.scopeKeys(f.scope)
	require.NoError(t, err)
	out := emptySessionState(f.scope)
	seq, err := f.repo.client.Get(t.Context(), s.sequence).Result()
	if !errors.Is(err, redis.Nil) {
		require.NoError(t, err)
		out.Sequence, err = strconv.ParseInt(seq, 10, 64)
		require.NoError(t, err)
	}
	ids, err := f.repo.client.SMembers(t.Context(), s.dags).Result()
	require.NoError(t, err)
	for _, id := range ids {
		d := s.dag(id)
		var meta sessionMeta
		require.NoError(t, json.Unmarshal([]byte(f.repo.client.Get(t.Context(), d.meta).Val()), &meta))
		view := &sessionDAG{ID: meta.ID, Generation: meta.Generation, State: meta.State, LastActive: meta.LastActive, Nodes: map[string]session.Node{}, Intents: map[string]session.Intent{}, Leases: map[string]session.Lease{}}
		for field, value := range f.repo.client.HGetAll(t.Context(), d.nodes).Val() {
			var n session.Node
			require.NoError(t, json.Unmarshal([]byte(value), &n))
			view.Nodes[field] = n
		}
		for field, value := range f.repo.client.HGetAll(t.Context(), d.intents).Val() {
			var i session.Intent
			require.NoError(t, json.Unmarshal([]byte(value), &i))
			view.Intents[field] = i
		}
		for field, value := range f.repo.client.HGetAll(t.Context(), d.leases).Val() {
			var l session.Lease
			require.NoError(t, json.Unmarshal([]byte(value), &l))
			view.Leases[field] = l
		}
		out.DAGs[id] = view
	}
	for field, value := range f.repo.client.HGetAll(t.Context(), s.messages).Val() {
		var ref session.NodeRef
		require.NoError(t, json.Unmarshal([]byte(value), &ref))
		out.Messages[field] = ref
	}
	for field, value := range f.repo.client.HGetAll(t.Context(), s.runs).Val() {
		var run sessionRunIndex
		require.NoError(t, json.Unmarshal([]byte(value), &run))
		if run.Status == sessionIntentPublished {
			out.Runs[field] = run.Ref
		}
	}
	return out
}

func sessionInjectFixture(t *testing.T, f *sessionFixture, fn func(*sessionState, int64) (bool, error)) error {
	t.Helper()
	state := sessionFixtureState(t, f)
	changed, err := fn(&state, f.now.UnixMilli())
	if err != nil {
		return err
	}
	if changed {
		sessionRestoreFixture(t, f, state)
	}
	return nil
}
