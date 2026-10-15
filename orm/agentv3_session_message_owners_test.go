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

func TestAgentV3SessionManifestMessageOwnersRetainEveryValidatedRef(t *testing.T) {
	dag := sessionRun(t)
	root := session.Node{Ref: session.NodeRef{DAGID: dag, NodeID: sessionRun(t)}, ReplyMessageIDs: []int{101, 102}}
	child := session.Node{Ref: session.NodeRef{DAGID: dag, NodeID: sessionRun(t)}, Parent: &root.Ref, ReplyMessageIDs: []int{101}}
	for _, nodes := range [][]session.Node{{root, child}, {child, root}} {
		t.Run(nodes[0].Ref.NodeID, func(t *testing.T) {
			owners, err := sessionManifestMessageOwners(t.Context(), nodes)
			require.NoError(t, err)
			require.True(t, owners.owns("101", root.Ref))
			require.True(t, owners.owns("101", child.Ref), "child takeover must survive either manifest order")
			require.True(t, owners.owns("102", root.Ref))
			require.False(t, owners.owns("102", child.Ref))
			require.False(t, owners.owns("101", session.NodeRef{DAGID: dag, NodeID: sessionRun(t)}))
			require.False(t, owners.owns("101", session.NodeRef{DAGID: sessionRun(t), NodeID: child.Ref.NodeID}))
			require.False(t, owners.owns("999", child.Ref))
		})
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	owners, err := sessionManifestMessageOwners(ctx, []session.Node{root, child})
	require.ErrorIs(t, err, context.Canceled)
	require.Nil(t, owners)
}

func TestAgentV3SessionGCSameDAGMessageTakeoverLastAndNonlast(t *testing.T) {
	for _, otherDAG := range []bool{false, true} {
		t.Run(fmt.Sprintf("otherDAG=%t", otherDAG), func(t *testing.T) {
			f := newSessionFixture(t, session.Options{TTL: time.Hour})
			root, err := f.service.Commit(t.Context(), sessionRequest(t, f.scope, "A", nil, 101, "root"))
			require.NoError(t, err)
			loaded := fixtureLoad(t, f, f.service, 101)
			child, err := f.service.Commit(t.Context(), sessionRequest(t, f.scope, "A", loaded.Parent, 101, "same message child"))
			require.NoError(t, err)
			require.NoError(t, loaded.Parent.Close())
			require.Equal(t, root.Ref.DAGID, child.Ref.DAGID)
			require.Equal(t, child.Ref, sessionReadState(t, f).Messages["101"])
			f.mr.SetTime(f.now.Add(2 * time.Hour))
			var survivor session.Node
			if otherDAG {
				survivor, err = f.service.Commit(t.Context(), sessionRequest(t, f.scope, "B", nil, 202, "survivor"))
				require.NoError(t, err)
			}
			require.NoError(t, f.service.Collect(t.Context()))
			view := sessionReadState(t, f)
			require.NotContains(t, view.DAGs, root.Ref.DAGID)
			require.NotContains(t, view.Messages, "101")
			for _, node := range []session.Node{root, child} {
				_, err := os.Stat(sessionArchivePath(f.dir, node))
				require.ErrorIs(t, err, os.ErrNotExist)
			}
			catalog, err := f.repo.Scopes(t.Context())
			require.NoError(t, err)
			if otherDAG {
				require.Len(t, view.DAGs, 1)
				require.Equal(t, survivor.Ref, view.Messages["202"])
				require.Equal(t, []session.Scope{f.scope}, catalog)
				_, err := os.Stat(sessionArchivePath(f.dir, survivor))
				require.NoError(t, err)
			} else {
				require.Empty(t, view.DAGs)
				require.Empty(t, view.Messages)
				require.Empty(t, catalog)
			}
		})
	}
}
