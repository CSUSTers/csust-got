package orm

import (
	"os"
	"strconv"
	"testing"
	"time"

	"csust-got/agent/session"

	"github.com/cloudwego/eino/schema"
	"github.com/stretchr/testify/require"
)

func sessionCompactedRequest(t testing.TB, scope session.Scope, source session.Node) session.CommitRequest {
	t.Helper()
	capture := session.TurnCapture{Complete: true, Bootstrap: session.History(schema.UserMessage("<session_summary>older turns</session_summary>")), Delta: session.History(schema.UserMessage("recent"), schema.AssistantMessage("recent answer", nil))}
	ref := source.Ref
	return session.CommitRequest{Scope: scope, Agent: source.Agent, RunID: sessionRun(t), Capture: capture, Receipt: session.DeliveryReceipt{MessageIDs: source.ReplyMessageIDs, RedirectFrom: &ref}}
}

func TestAgentV3SessionChainReadsAncestorsWithoutActivity(t *testing.T) {
	f := newSessionFixture(t, session.Options{})
	root, err := f.service.Commit(t.Context(), sessionRequest(t, f.scope, "A", nil, 101, "root"))
	require.NoError(t, err)
	loaded := fixtureLoad(t, f, f.service, 101)
	child, err := f.service.Commit(t.Context(), sessionRequest(t, f.scope, "A", loaded.Parent, 102, "child"))
	require.NoError(t, err)
	before := sessionReadState(t, f)
	f.mr.SetTime(f.now.Add(time.Hour))
	chain, err := f.repo.Chain(t.Context(), f.scope, child.Ref)
	require.NoError(t, err)
	require.Equal(t, []session.Node{root, child}, chain)
	turns, err := f.service.Replay(t.Context(), f.scope, child.Ref)
	require.NoError(t, err)
	require.Len(t, turns, 2)
	require.Equal(t, root, turns[0].Node)
	require.Len(t, turns[0].Delta, 2)
	require.Equal(t, "child", turns[1].Delta[0].Content)
	require.Equal(t, before, sessionReadState(t, f), "read-only chain and replay neither pin nor refresh activity")
	_, err = f.repo.Chain(t.Context(), f.scope, session.NodeRef{DAGID: root.Ref.DAGID, NodeID: sessionRun(t)})
	require.ErrorIs(t, err, session.ErrMiss)
	_, err = f.repo.Chain(t.Context(), f.scope, session.NodeRef{DAGID: sessionRun(t), NodeID: sessionRun(t)})
	require.ErrorIs(t, err, session.ErrMiss)
}

func TestAgentV3SessionRedirectPublishMovesOnlyMappedMessages(t *testing.T) {
	tests := []struct {
		name         string
		prepare      func(t *testing.T, f *sessionFixture, source session.Node)
		wantMoved    []int
		wantLatest   bool
		wantStaleErr bool
	}{
		{name: "all mapped", wantMoved: []int{201, 202}, wantLatest: true},
		{name: "one remapped", prepare: func(t *testing.T, f *sessionFixture, source session.Node) {
			s, err := f.repo.scopeKeys(f.scope)
			require.NoError(t, err)
			other := session.NodeRef{DAGID: source.Ref.DAGID, NodeID: sessionRun(t)}
			require.NoError(t, f.repo.client.HSet(t.Context(), s.messages, "202", sessionEncode(other)).Err())
		}, wantMoved: []int{201}, wantLatest: true},
		{name: "newer latest keeps precedence", prepare: func(t *testing.T, f *sessionFixture, source session.Node) {
			_, err := f.service.Commit(t.Context(), sessionRequest(t, f.scope, source.Agent, nil, 301, "newer root"))
			require.NoError(t, err)
		}, wantMoved: []int{201, 202}},
		{name: "all moved", prepare: func(t *testing.T, f *sessionFixture, source session.Node) {
			s, err := f.repo.scopeKeys(f.scope)
			require.NoError(t, err)
			require.NoError(t, f.repo.client.HDel(t.Context(), s.messages, "201", "202").Err())
		}, wantStaleErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newSessionFixture(t, session.Options{})
			req := sessionRequest(t, f.scope, "A", nil, 201, "source")
			req.Receipt.MessageIDs = []int{201, 202}
			source, err := f.service.Commit(t.Context(), req)
			require.NoError(t, err)
			if tt.prepare != nil {
				tt.prepare(t, f, source)
			}
			before := sessionReadState(t, f)
			compacted, err := f.service.Commit(t.Context(), sessionCompactedRequest(t, f.scope, source))
			if tt.wantStaleErr {
				require.ErrorIs(t, err, session.ErrStale)
				after := sessionReadState(t, f)
				require.Equal(t, before.Messages, after.Messages)
				require.Equal(t, before.Runs, after.Runs, "the aborted attempt leaves no published run")
				require.Equal(t, before.DAGs[source.Ref.DAGID], after.DAGs[source.Ref.DAGID])
				for id, dag := range after.DAGs {
					if id != source.Ref.DAGID {
						require.Empty(t, dag.Nodes, "a stale redirect publishes nothing; the empty root DAG waits for scheduled collection")
						require.Empty(t, dag.Intents)
					}
				}
				return
			}
			require.NoError(t, err)
			require.Nil(t, compacted.Parent)
			require.NotEqual(t, source.Ref.DAGID, compacted.Ref.DAGID)
			require.Equal(t, &source.Ref, compacted.RedirectedFrom)
			require.Equal(t, tt.wantMoved, compacted.ReplyMessageIDs)
			state := sessionReadState(t, f)
			for _, id := range tt.wantMoved {
				require.Equal(t, compacted.Ref, state.Messages[strconv.Itoa(id)])
			}
			require.Equal(t, before.DAGs[source.Ref.DAGID], state.DAGs[source.Ref.DAGID], "the compacted DAG is not rewritten")
			require.Equal(t, compacted, state.DAGs[compacted.Ref.DAGID].Nodes[compacted.Ref.NodeID])
			latest, err := f.service.Load(t.Context(), session.Selection{Scope: f.scope, Agent: "A", Mode: session.SelectLatest, LoadOnly: true})
			require.NoError(t, err)
			t.Cleanup(func() { _ = latest.Parent.Close() })
			if tt.wantLatest {
				require.Equal(t, compacted.Ref, latest.Parent.Ref())
				require.Equal(t, "<session_summary>older turns</session_summary>", latest.Messages[0].Content)
			} else {
				require.NotEqual(t, compacted.Ref, latest.Parent.Ref())
			}
			replay, err := f.service.Load(t.Context(), session.Selection{Scope: f.scope, Mode: session.SelectReply, ReplyMessageID: tt.wantMoved[0], LoadOnly: true})
			require.NoError(t, err)
			t.Cleanup(func() { _ = replay.Parent.Close() })
			require.Equal(t, compacted.Ref, replay.Parent.Ref())
			require.True(t, replay.Parent.ContainsReplyMessageID(tt.wantMoved[0]))
			require.Len(t, replay.Messages, 3)
			again, err := f.service.Commit(t.Context(), session.CommitRequest{Scope: f.scope, Agent: compacted.Agent, RunID: compacted.RunID, Capture: sessionCompactedRequest(t, f.scope, source).Capture, Receipt: session.DeliveryReceipt{MessageIDs: source.ReplyMessageIDs, RedirectFrom: &source.Ref}})
			require.NoError(t, err)
			require.Equal(t, compacted, again, "the same RunID returns the published node")
		})
	}
}

func TestAgentV3SessionRedirectRejectsChildAndInvalidSource(t *testing.T) {
	f := newSessionFixture(t, session.Options{})
	source, err := f.service.Commit(t.Context(), sessionRequest(t, f.scope, "A", nil, 201, "source"))
	require.NoError(t, err)
	loaded := fixtureLoad(t, f, f.service, 201)
	req := sessionCompactedRequest(t, f.scope, source)
	req.Parent = loaded.Parent
	req.Capture.Bootstrap = nil
	_, err = f.service.Commit(t.Context(), req)
	require.ErrorIs(t, err, session.ErrCorrupt)
	req = sessionCompactedRequest(t, f.scope, source)
	req.Receipt.RedirectFrom = &session.NodeRef{DAGID: "bad", NodeID: "bad"}
	_, err = f.service.Commit(t.Context(), req)
	require.ErrorIs(t, err, session.ErrCorrupt)
}

func TestAgentV3SessionRedirectSurvivesSourceCollection(t *testing.T) {
	f := newSessionFixture(t, session.Options{TTL: 2 * time.Hour})
	source, err := f.service.Commit(t.Context(), sessionRequest(t, f.scope, "A", nil, 201, "source"))
	require.NoError(t, err)
	f.mr.SetTime(f.now.Add(time.Hour))
	compacted, err := f.service.Commit(t.Context(), sessionCompactedRequest(t, f.scope, source))
	require.NoError(t, err)
	f.mr.SetTime(f.now.Add(2*time.Hour + time.Second))
	require.NoError(t, f.service.Collect(t.Context()))
	state := sessionReadState(t, f)
	require.NotContains(t, state.DAGs, source.Ref.DAGID, "the idle source DAG is collected by TTL")
	require.Contains(t, state.DAGs, compacted.Ref.DAGID)
	require.Equal(t, compacted.Ref, state.Messages["201"], "collection compares NodeRef and keeps the redirected mapping")
	_, err = os.Stat(sessionArchivePath(f.dir, source))
	require.ErrorIs(t, err, os.ErrNotExist)
	_, err = os.Stat(sessionArchivePath(f.dir, compacted))
	require.NoError(t, err)
}
