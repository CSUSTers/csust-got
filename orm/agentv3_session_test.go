package orm

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"csust-got/agent/session"

	"github.com/cloudwego/eino/schema"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

type sessionFixture struct {
	mr      sessionTestRedis
	repo    *AgentV3SessionRepository
	service *session.Service
	dir     string
	scope   session.Scope
	now     time.Time
}

type sessionTestRedis interface {
	Addr() string
	SetTime(time.Time)
	Keys() []string
	TTL(string) time.Duration
	configureClient(*redis.Client)
}

func newSessionFixture(t testing.TB, options session.Options) *sessionFixture {
	t.Helper()
	mr := newSessionTestRedis(t)
	now := time.Date(2026, 10, 7, 10, 0, 0, 0, time.UTC)
	mr.SetTime(now)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr(), MaxRetries: -1})
	mr.configureClient(client)
	t.Cleanup(func() { _ = client.Close() })
	repo, err := NewAgentV3SessionRepository(client, "session-test:")
	require.NoError(t, err)
	dir := t.TempDir()
	svc := fixtureService(t, repo, dir, options)
	return &sessionFixture{mr: mr, repo: repo, service: svc, dir: dir, scope: session.Scope{Namespace: repo.Namespace(), Bot: "bot", Platform: "telegram", ChatID: -100}, now: now}
}

func fixtureService(t testing.TB, repo session.Repository, dir string, options session.Options) *session.Service {
	t.Helper()
	files, err := session.NewFileStore(dir)
	require.NoError(t, err)
	svc, err := session.NewService(repo, files, options)
	if err != nil {
		require.NoError(t, files.Close())
	}
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })
	return svc
}

func sessionRun(t testing.TB) string {
	t.Helper()
	id, err := session.NewID()
	require.NoError(t, err)
	return id
}

func sessionCapture(text string) session.TurnCapture {
	return session.TurnCapture{Frame: []session.Record{{Source: session.SourceFrame, Message: schema.SystemMessage("new agent system")}}, Delta: session.History(schema.UserMessage(text), schema.AssistantMessage("answer "+text, nil)), Complete: true}
}

func sessionRequest(t testing.TB, scope session.Scope, agent string, parent *session.LoadedParent, messageID int, text string) session.CommitRequest {
	t.Helper()
	return session.CommitRequest{Scope: scope, Agent: agent, RunID: sessionRun(t), Parent: parent, Capture: sessionCapture(text), Receipt: session.DeliveryReceipt{MessageIDs: []int{messageID}}}
}

func fixtureLoad(t *testing.T, f *sessionFixture, svc *session.Service, messageID int) session.LoadResult {
	t.Helper()
	r, err := svc.Load(t.Context(), session.Selection{Scope: f.scope, Agent: "other-agent", Mode: session.SelectReply, ReplyMessageID: messageID})
	require.NoError(t, err)
	t.Cleanup(func() { _ = r.Parent.Close() })
	return r
}

func sessionArchivePath(dir string, n session.Node) string {
	b, _ := json.Marshal([]string{n.Scope.Bot, n.Scope.Platform})
	h := sha256.Sum256(b)
	return filepath.Join(dir, n.Scope.Namespace, hex.EncodeToString(h[:]), strconv.FormatInt(n.Scope.ChatID, 10), n.Ref.DAGID, n.FileName)
}

func sessionReadState(t testing.TB, f *sessionFixture) sessionState {
	t.Helper()
	return sessionFixtureState(t, f)
}

func TestAgentV3SessionBranchesSelectionAndIsolation(t *testing.T) {
	f := newSessionFixture(t, session.Options{})
	req := sessionRequest(t, f.scope, "A", nil, 101, "root")
	req.Capture.Bootstrap = session.History(schema.UserMessage("fallback"), schema.AssistantMessage("fallback answer", nil))
	req.Capture.Delta = session.History(schema.UserMessage("root"), &schema.Message{Role: schema.Assistant, ToolCalls: []schema.ToolCall{{ID: "call", Function: schema.FunctionCall{Name: "search", Arguments: `{"q":"x"}`}}}, ReasoningContent: "reason"}, &schema.Message{Role: schema.Tool, ToolCallID: "call", ToolName: "search", Content: strings.Repeat("tool", 40000)}, schema.AssistantMessage("root answer", nil))
	root, err := f.service.Commit(t.Context(), req)
	require.NoError(t, err)
	replayed, err := f.service.Commit(t.Context(), req)
	require.NoError(t, err)
	require.Equal(t, root.Ref, replayed.Ref)
	p1 := fixtureLoad(t, f, f.service, 101)
	require.Len(t, p1.Messages, 6)
	require.Len(t, p1.Messages[2+1].ToolCalls, 1)
	client2 := redis.NewClient(&redis.Options{Addr: f.mr.Addr()})
	f.mr.configureClient(client2)
	t.Cleanup(func() { _ = client2.Close() })
	repo2, err := NewAgentV3SessionRepository(client2, "session-test:")
	require.NoError(t, err)
	svc2 := fixtureService(t, repo2, f.dir, session.Options{})
	p2 := fixtureLoad(t, f, svc2, 101)
	b1, err := f.service.Commit(t.Context(), sessionRequest(t, f.scope, "B", p1.Parent, 102, "branch one"))
	require.NoError(t, err)
	b2, err := svc2.Commit(t.Context(), sessionRequest(t, f.scope, "B", p2.Parent, 103, "branch two"))
	require.NoError(t, err)
	require.Equal(t, root.Ref, *b1.Parent)
	require.Equal(t, root.Ref, *b2.Parent)
	require.Equal(t, b1.Ref.DAGID, b2.Ref.DAGID)
	require.NotEqual(t, b1.Ref.NodeID, b2.Ref.NodeID)
	require.Greater(t, b2.CommitSequence, b1.CommitSequence)
	branch := fixtureLoad(t, f, svc2, 102)
	require.Len(t, branch.Messages, 8)
	require.Equal(t, "branch one", branch.Messages[6].Content)
	for _, msg := range branch.Messages {
		require.NotContains(t, msg.Content, "branch two")
		require.NotEqual(t, schema.System, msg.Role)
	}
	data, err := os.ReadFile(sessionArchivePath(f.dir, b1))
	require.NoError(t, err)
	require.NotContains(t, string(data), "fallback answer")
	require.NotContains(t, string(data), strings.Repeat("tool", 100))
	latest, err := svc2.Load(t.Context(), session.Selection{Scope: f.scope, Agent: "B", Mode: session.SelectLatest})
	require.NoError(t, err)
	defer latest.Parent.Close()
	require.Equal(t, b2.Ref, latest.Parent.Ref())
	a, err := svc2.Load(t.Context(), session.Selection{Scope: f.scope, Agent: "A", Mode: session.SelectLatest})
	require.NoError(t, err)
	defer a.Parent.Close()
	require.Equal(t, root.Ref, a.Parent.Ref())
	_, err = svc2.Load(t.Context(), session.Selection{Scope: f.scope, Agent: "B", Mode: session.SelectReply, ReplyMessageID: 999})
	require.ErrorIs(t, err, session.ErrMiss)
	for _, scope := range []session.Scope{
		{Bot: "another", Platform: f.scope.Platform, ChatID: f.scope.ChatID},
		{Bot: f.scope.Bot, Platform: "another", ChatID: f.scope.ChatID},
		{Bot: f.scope.Bot, Platform: f.scope.Platform, ChatID: f.scope.ChatID + 1},
	} {
		_, err = svc2.Load(t.Context(), session.Selection{Scope: scope, Agent: "B", Mode: session.SelectReply, ReplyMessageID: 101})
		require.ErrorIs(t, err, session.ErrMiss)
	}
	otherRepo, err := NewAgentV3SessionRepository(client2, "other-prefix:")
	require.NoError(t, err)
	other := fixtureService(t, otherRepo, f.dir, session.Options{})
	wrong := f.scope
	wrong.Namespace = ""
	_, err = other.Load(t.Context(), session.Selection{Scope: wrong, Mode: session.SelectReply, ReplyMessageID: 101})
	require.ErrorIs(t, err, session.ErrMiss)
	for _, key := range f.mr.Keys() {
		require.Zero(t, f.mr.TTL(key), "lifecycle metadata must not naturally expire")
	}
}

func TestAgentV3SessionBrokenChainNeverReturnsParent(t *testing.T) {
	for _, kind := range []string{"missing-file", "truncated", "cycle", "cross-scope", "missing-parent", "version", "digest"} {
		t.Run(kind, func(t *testing.T) {
			f := newSessionFixture(t, session.Options{})
			root, err := f.service.Commit(t.Context(), sessionRequest(t, f.scope, "A", nil, 101, "root"))
			require.NoError(t, err)
			switch kind {
			case "missing-file":
				require.NoError(t, os.Remove(sessionArchivePath(f.dir, root)))
			case "truncated":
				require.NoError(t, os.WriteFile(sessionArchivePath(f.dir, root), []byte("{}\n"), 0600))
			default:
				require.NoError(t, sessionInjectFixture(t, f, func(s *sessionState, _ int64) (bool, error) {
					d := s.DAGs[root.Ref.DAGID]
					n := d.Nodes[root.Ref.NodeID]
					switch kind {
					case "cycle":
						n.Parent = &root.Ref
					case "cross-scope":
						n.Scope.ChatID++
					case "missing-parent":
						n.Parent = &session.NodeRef{DAGID: root.Ref.DAGID, NodeID: strings.Repeat("a", 32)}
					case "version":
						n.Version++
					case "digest":
						n.Digest = strings.Repeat("0", 64)
					}
					d.Nodes[n.Ref.NodeID] = n
					return true, nil
				}))
			}
			loaded, err := f.service.Load(t.Context(), session.Selection{Scope: f.scope, Mode: session.SelectReply, ReplyMessageID: 101})
			require.ErrorIs(t, err, session.ErrCorrupt)
			require.Nil(t, loaded.Parent)
			require.Nil(t, loaded.Messages)
			fallback := sessionRequest(t, f.scope, "A", nil, 102, "fallback input")
			fallback.Capture.Bootstrap = session.History(schema.UserMessage("fallback baseline"), schema.AssistantMessage("baseline answer", nil))
			next, err := f.service.Commit(t.Context(), fallback)
			require.NoError(t, err)
			require.Nil(t, next.Parent)
			require.NotEqual(t, root.Ref.DAGID, next.Ref.DAGID)
		})
	}
}

func TestAgentV3SessionIdleGCAndReferenceComparison(t *testing.T) {
	f := newSessionFixture(t, session.Options{TTL: time.Hour})
	old, err := f.service.Commit(t.Context(), sessionRequest(t, f.scope, "A", nil, 101, "old"))
	require.NoError(t, err)
	parent := fixtureLoad(t, f, f.service, 101)
	child, err := f.service.Commit(t.Context(), sessionRequest(t, f.scope, "B", parent.Parent, 102, "child"))
	require.NoError(t, err)
	require.NoError(t, parent.Parent.Close())
	f.mr.SetTime(f.now.Add(2 * time.Hour))
	live, err := f.service.Commit(t.Context(), sessionRequest(t, f.scope, "A", nil, 101, "new mapping"))
	require.NoError(t, err)
	require.NoError(t, f.service.Collect(t.Context()))
	for _, n := range []session.Node{old, child} {
		_, err = os.Stat(sessionArchivePath(f.dir, n))
		require.ErrorIs(t, err, os.ErrNotExist)
	}
	_, err = os.Stat(sessionArchivePath(f.dir, live))
	require.NoError(t, err)
	loaded := fixtureLoad(t, f, f.service, 101)
	require.Equal(t, live.Ref, loaded.Parent.Ref())
	state := sessionReadState(t, f)
	require.Len(t, state.DAGs, 1)
	require.Len(t, state.Messages, 1)
	require.NotContains(t, state.Runs, old.RunID)
	require.NotContains(t, state.Runs, child.RunID)
	require.NoError(t, loaded.Parent.Close())
	f.mr.SetTime(f.now.Add(4 * time.Hour))
	require.NoError(t, f.service.Collect(t.Context()))
	require.Empty(t, sessionReadState(t, f).DAGs)
	scopes, err := f.repo.Scopes(t.Context())
	require.NoError(t, err)
	require.Empty(t, scopes)
}

func TestAgentV3SessionLeaseRenewalAndExpiredParentRoot(t *testing.T) {
	f := newSessionFixture(t, session.Options{TTL: time.Minute, LeaseDuration: time.Hour, RenewInterval: 20 * time.Millisecond})
	root, err := f.service.Commit(t.Context(), sessionRequest(t, f.scope, "A", nil, 101, "root"))
	require.NoError(t, err)
	parent := fixtureLoad(t, f, f.service, 101)
	lastActive := sessionReadState(t, f).DAGs[root.Ref.DAGID].LastActive
	parent.Messages[0].Content = "caller mutated replay" // must not change the private fallback baseline
	f.mr.SetTime(f.now.Add(10 * time.Minute))
	require.Eventually(t, func() bool {
		state := sessionReadState(t, f)
		for _, lease := range state.DAGs[root.Ref.DAGID].Leases {
			if lease.Deadline > f.now.Add(time.Hour).UnixMilli() {
				return true
			}
		}
		return false
	}, time.Second, 10*time.Millisecond)
	require.Equal(t, lastActive, sessionReadState(t, f).DAGs[root.Ref.DAGID].LastActive, "heartbeat is not user activity")
	require.NoError(t, f.service.Collect(t.Context()))
	_, err = os.Stat(sessionArchivePath(f.dir, root))
	require.NoError(t, err)
	// Simulate a lost lease under the same atomic seam used by GC; the retained
	// LoadedParent proof can no longer connect to the old DAG.
	require.NoError(t, sessionInjectFixture(t, f, func(s *sessionState, _ int64) (bool, error) {
		s.DAGs[root.Ref.DAGID].Leases = map[string]session.Lease{}
		return true, nil
	}))
	f.mr.SetTime(f.now.Add(2 * time.Hour))
	require.NoError(t, f.service.Collect(t.Context()))
	next, err := f.service.Commit(t.Context(), sessionRequest(t, f.scope, "A", parent.Parent, 102, "after loss"))
	require.NoError(t, err)
	require.Nil(t, next.Parent)
	require.NotEqual(t, root.Ref.DAGID, next.Ref.DAGID)
	loaded := fixtureLoad(t, f, f.service, 102)
	require.Len(t, loaded.Messages, 4)
	require.Equal(t, "root", loaded.Messages[0].Content)
	require.Equal(t, "after loss", loaded.Messages[2].Content)
}

func TestAgentV3SessionLoadReactivatesAndConcurrentBranches(t *testing.T) {
	f := newSessionFixture(t, session.Options{TTL: time.Hour, LeaseDuration: time.Hour})
	root, err := f.service.Commit(t.Context(), sessionRequest(t, f.scope, "A", nil, 101, "root"))
	require.NoError(t, err)
	f.mr.SetTime(f.now.Add(2 * time.Hour))
	loaded := fixtureLoad(t, f, f.service, 101)
	require.NoError(t, f.service.Collect(t.Context()))
	require.Equal(t, f.now.Add(2*time.Hour).UnixMilli(), sessionReadState(t, f).DAGs[root.Ref.DAGID].LastActive)
	requests := []session.CommitRequest{sessionRequest(t, f.scope, "A", loaded.Parent, 102, "one"), sessionRequest(t, f.scope, "A", loaded.Parent, 103, "two")}
	var nodes [2]session.Node
	var failures [2]error
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range requests {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			nodes[i], failures[i] = f.service.Commit(t.Context(), requests[i])
		}()
	}
	close(start)
	wg.Wait()
	for i := range requests {
		require.NoError(t, failures[i])
		require.Equal(t, root.Ref, *nodes[i].Parent)
	}
	latest, err := f.service.Load(t.Context(), session.Selection{Scope: f.scope, Agent: "A", Mode: session.SelectLatest})
	require.NoError(t, err)
	defer latest.Parent.Close()
	expected := nodes[0]
	if nodes[1].CommitSequence > expected.CommitSequence {
		expected = nodes[1]
	}
	require.Equal(t, expected.Ref, latest.Parent.Ref())
}

type publicationFault struct {
	session.Repository
	publishApplied bool
	failPublish    bool
	failQueries    bool
	failAbort      bool
}

var (
	errSessionLostResponse   = errors.New("injected lost Redis EXEC response")
	errSessionDisconnected   = errors.New("injected disconnected Redis")
	errSessionAbortUnknown   = errors.New("injected abort result uncertainty")
	errSessionReleaseFailure = errors.New("injected pin release failure")
)

func (r *publicationFault) Publish(ctx context.Context, scope session.Scope, intent session.Intent, digest string, size int64, receipt session.DeliveryReceipt) (session.Node, error) {
	if r.failPublish {
		return session.Node{}, session.ErrFence
	}
	n, err := r.Repository.Publish(ctx, scope, intent, digest, size, receipt)
	if err != nil {
		return n, err
	}
	r.publishApplied = true
	return session.Node{}, errSessionLostResponse
}

func (r *publicationFault) GetPublication(ctx context.Context, scope session.Scope, run string) (*session.Node, error) {
	if r.publishApplied && r.failQueries {
		return nil, errSessionDisconnected
	}
	return r.Repository.GetPublication(ctx, scope, run)
}

func (r *publicationFault) AbortIntent(ctx context.Context, scope session.Scope, intent session.Intent) (bool, error) {
	if r.failAbort {
		return false, errSessionAbortUnknown
	}
	return r.Repository.AbortIntent(ctx, scope, intent)
}

func TestAgentV3SessionPublicationCompensation(t *testing.T) {
	for _, kind := range []string{"known-failure", "lost-response", "unknown-published", "unknown-unpublished"} {
		t.Run(kind, func(t *testing.T) {
			f := newSessionFixture(t, session.Options{})
			fault := &publicationFault{Repository: f.repo, failPublish: kind == "known-failure" || kind == "unknown-unpublished", failQueries: kind == "unknown-published", failAbort: kind == "unknown-unpublished"}
			svc := fixtureService(t, fault, f.dir, session.Options{})
			req := sessionRequest(t, f.scope, "A", nil, 101, "answer delivered once")
			node, err := svc.Commit(t.Context(), req)
			switch kind {
			case "known-failure":
				require.ErrorIs(t, err, session.ErrFence)
				pending, err := f.repo.Pending(t.Context(), f.scope)
				require.NoError(t, err)
				require.Empty(t, pending)
				published, err := f.repo.GetPublication(t.Context(), f.scope, req.RunID)
				require.NoError(t, err)
				require.Nil(t, published)
			case "lost-response":
				require.NoError(t, err)
				published, err := f.repo.GetPublication(t.Context(), f.scope, req.RunID)
				require.NoError(t, err)
				require.Equal(t, node.Ref, published.Ref)
				_, err = os.Stat(sessionArchivePath(f.dir, node))
				require.NoError(t, err)
			case "unknown-published", "unknown-unpublished":
				require.ErrorIs(t, err, session.ErrUnknown)
				pending, err := f.repo.Pending(t.Context(), f.scope)
				require.NoError(t, err)
				require.Len(t, pending, 1)
				path := sessionArchivePath(f.dir, pending[0].Node)
				_, err = os.Stat(path)
				require.NoError(t, err, "uncertainty must retain files")
				require.NoError(t, svc.Close())
				restarted := fixtureService(t, f.repo, f.dir, session.Options{})
				require.NoError(t, restarted.Recover(t.Context()))
				if kind == "unknown-published" {
					loaded := fixtureLoad(t, f, restarted, 101)
					require.Len(t, loaded.Messages, 2)
					_, err = os.Stat(path)
					require.NoError(t, err)
				} else {
					_, err = os.Stat(path)
					require.ErrorIs(t, err, os.ErrNotExist)
				}
			}
		})
	}
}

func TestAgentV3SessionDurableIntentAndDeletingRecovery(t *testing.T) {
	f := newSessionFixture(t, session.Options{TTL: time.Hour})
	files, err := session.NewFileStore(f.dir)
	require.NoError(t, err)
	defer files.Close()
	var intent session.Intent
	require.NoError(t, files.WithScopeLock(t.Context(), f.scope, func(fs *session.ScopeFiles) error {
		var err error
		intent, err = f.repo.Reserve(t.Context(), session.Reservation{Scope: f.scope, Agent: "A", RunID: sessionRun(t)}, time.Minute)
		require.NoError(t, err)
		_, _, err = fs.WriteAtomic(intent.Node, sessionCapture("crash before publish"))
		return err
	}))
	_, err = os.Stat(sessionArchivePath(f.dir, intent.Node))
	require.NoError(t, err)
	require.NoError(t, f.service.Recover(t.Context()))
	_, err = os.Stat(sessionArchivePath(f.dir, intent.Node))
	require.ErrorIs(t, err, os.ErrNotExist)
	root, err := f.service.Commit(t.Context(), sessionRequest(t, f.scope, "A", nil, 101, "root"))
	require.NoError(t, err)
	parent := fixtureLoad(t, f, f.service, 101)
	child, err := f.service.Commit(t.Context(), sessionRequest(t, f.scope, "A", parent.Parent, 102, "child"))
	require.NoError(t, err)
	require.NoError(t, parent.Parent.Close())
	f.mr.SetTime(f.now.Add(2 * time.Hour))
	require.NoError(t, files.WithScopeLock(t.Context(), f.scope, func(fs *session.ScopeFiles) error {
		deletions, err := f.repo.ClaimDeleting(t.Context(), f.scope, time.Hour)
		require.NoError(t, err)
		require.NotEmpty(t, deletions)
		return fs.Remove(root) // crash between deletion of two manifest entries
	}))
	_, err = f.service.Load(t.Context(), session.Selection{Scope: f.scope, Mode: session.SelectReply, ReplyMessageID: 102})
	require.ErrorIs(t, err, session.ErrMiss)
	restarted := fixtureService(t, f.repo, f.dir, session.Options{TTL: time.Hour})
	require.NoError(t, restarted.Recover(t.Context()))
	_, err = os.Stat(sessionArchivePath(f.dir, child))
	require.ErrorIs(t, err, os.ErrNotExist)
	require.Empty(t, sessionReadState(t, f).DAGs)
	require.NoError(t, restarted.Close())
	require.NoError(t, restarted.Close())
}

func TestAgentV3SessionFenceAndIncompleteDelivery(t *testing.T) {
	f := newSessionFixture(t, session.Options{})
	req := sessionRequest(t, f.scope, "A", nil, 101, "root")
	req.Capture.Complete = false
	_, err := f.service.Commit(t.Context(), req)
	require.ErrorIs(t, err, session.ErrCorrupt)
	req.Capture.Complete = true
	req.Receipt.MessageIDs = nil
	_, err = f.service.Commit(t.Context(), req)
	require.ErrorIs(t, err, session.ErrCorrupt)
	req.Receipt.MessageIDs = []int{0}
	_, err = f.service.Commit(t.Context(), req)
	require.ErrorIs(t, err, session.ErrCorrupt)
	require.Empty(t, sessionReadState(t, f).DAGs)
	root, err := f.service.Commit(t.Context(), sessionRequest(t, f.scope, "A", nil, 101, "root"))
	require.NoError(t, err)
	pinned, err := f.repo.ResolveAndPin(t.Context(), session.Selection{Scope: f.scope, Mode: session.SelectReply, ReplyMessageID: 101}, sessionRun(t), time.Minute)
	require.NoError(t, err)
	lease := pinned.Lease
	lease.Token = sessionRun(t)
	require.ErrorIs(t, f.repo.Renew(t.Context(), f.scope, lease, time.Minute), session.ErrFence)
	intent, err := f.repo.Reserve(t.Context(), session.Reservation{Scope: f.scope, Agent: "A", RunID: sessionRun(t), Parent: &root.Ref, Lease: &pinned.Lease}, time.Minute)
	require.NoError(t, err)
	f.mr.SetTime(f.now.Add(2 * time.Minute))
	_, err = f.repo.Publish(t.Context(), f.scope, intent, strings.Repeat("a", 64), 10, session.DeliveryReceipt{MessageIDs: []int{102}})
	require.ErrorIs(t, err, session.ErrFence)
}

type pausedReservation struct {
	session.Repository
	reserved chan session.Intent
	resume   chan struct{}
}

func (r *pausedReservation) Reserve(ctx context.Context, req session.Reservation, ttl time.Duration) (session.Intent, error) {
	intent, err := r.Repository.Reserve(ctx, req, ttl)
	if err != nil {
		return intent, err
	}
	r.reserved <- intent
	select {
	case <-r.resume:
		return intent, nil
	case <-ctx.Done():
		return intent, ctx.Err()
	}
}

func TestAgentV3SessionPausedWriterCannotRaceGC(t *testing.T) {
	options := session.Options{TTL: time.Minute, LeaseDuration: time.Minute, RenewInterval: 50 * time.Second, OperationTimeout: 5 * time.Second}
	f := newSessionFixture(t, options)
	root, err := f.service.Commit(t.Context(), sessionRequest(t, f.scope, "A", nil, 101, "root"))
	require.NoError(t, err)
	paused := &pausedReservation{Repository: f.repo, reserved: make(chan session.Intent, 1), resume: make(chan struct{})}
	writer := fixtureService(t, paused, f.dir, options)
	parent := fixtureLoad(t, f, writer, 101)
	request := sessionRequest(t, f.scope, "A", parent.Parent, 102, "slow writer")
	writeDone := make(chan error, 1)
	go func() { _, err := writer.Commit(t.Context(), request); writeDone <- err }()
	var intent session.Intent
	select {
	case intent = <-paused.reserved:
	case <-time.After(time.Second):
		t.Fatal("writer did not reserve")
	}
	f.mr.SetTime(f.now.Add(2 * time.Hour))
	client := redis.NewClient(&redis.Options{Addr: f.mr.Addr()})
	f.mr.configureClient(client)
	t.Cleanup(func() { _ = client.Close() })
	repo, err := NewAgentV3SessionRepository(client, "session-test:")
	require.NoError(t, err)
	collector := fixtureService(t, repo, f.dir, options)
	collectDone := make(chan error, 1)
	go func() { collectDone <- collector.Collect(t.Context()) }()
	select {
	case err := <-collectDone:
		t.Fatalf("GC crossed writer's kernel lock: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	_, err = os.Stat(sessionArchivePath(f.dir, root))
	require.NoError(t, err)
	close(paused.resume)
	require.ErrorIs(t, <-writeDone, session.ErrFence)
	require.NoError(t, <-collectDone)
	for _, n := range []session.Node{root, intent.Node} {
		_, err = os.Stat(sessionArchivePath(f.dir, n))
		require.ErrorIs(t, err, os.ErrNotExist)
	}
	require.Empty(t, sessionReadState(t, f).DAGs)
}

func TestAgentV3SessionGCRejectsCrossDAGManifest(t *testing.T) {
	f := newSessionFixture(t, session.Options{TTL: time.Hour})
	old, err := f.service.Commit(t.Context(), sessionRequest(t, f.scope, "A", nil, 101, "old"))
	require.NoError(t, err)
	f.mr.SetTime(f.now.Add(2 * time.Hour))
	live, err := f.service.Commit(t.Context(), sessionRequest(t, f.scope, "B", nil, 102, "live"))
	require.NoError(t, err)
	require.NoError(t, sessionInjectFixture(t, f, func(s *sessionState, _ int64) (bool, error) {
		s.DAGs[old.Ref.DAGID].Nodes[old.Ref.NodeID] = live
		return true, nil
	}))
	require.ErrorIs(t, f.service.Collect(t.Context()), session.ErrCorrupt)
	_, err = os.Stat(sessionArchivePath(f.dir, live))
	require.NoError(t, err)
	loaded := fixtureLoad(t, f, f.service, 102)
	require.Equal(t, live.Ref, loaded.Parent.Ref())
}

func TestAgentV3SessionRecoverOnlyExistingWork(t *testing.T) {
	f := newSessionFixture(t, session.Options{TTL: time.Hour})
	files, err := session.NewFileStore(f.dir)
	require.NoError(t, err)
	defer files.Close()
	f.mr.SetTime(f.now.Add(-2 * time.Hour))
	deleting, err := f.service.Commit(t.Context(), sessionRequest(t, f.scope, "old", nil, 100, "already deleting"))
	require.NoError(t, err)
	f.mr.SetTime(f.now)
	require.NoError(t, files.WithScopeLock(t.Context(), f.scope, func(*session.ScopeFiles) error {
		claimed, err := f.repo.ClaimDeleting(t.Context(), f.scope, time.Hour)
		require.NoError(t, err)
		require.Len(t, claimed, 1)
		require.Equal(t, deleting.Ref.DAGID, claimed[0].DAGID)
		return nil // a scheduled collector crashed after marking deleting
	}))
	active, err := f.service.Commit(t.Context(), sessionRequest(t, f.scope, "normal", nil, 101, "ordinary active DAG"))
	require.NoError(t, err)
	var pending session.Intent
	require.NoError(t, files.WithScopeLock(t.Context(), f.scope, func(fs *session.ScopeFiles) error {
		var err error
		pending, err = f.repo.Reserve(t.Context(), session.Reservation{Scope: f.scope, Agent: "pending", RunID: sessionRun(t)}, time.Minute)
		if err != nil {
			return err
		}
		_, _, err = fs.WriteAtomic(pending.Node, sessionCapture("unpublished file"))
		return err
	}))
	before := sessionReadState(t, f).DAGs[active.Ref.DAGID]
	f.mr.SetTime(f.now.Add(2 * time.Hour)) // ordinary DAG has expired, but this is not a daily collection
	for range 3 {
		require.NoError(t, f.service.Recover(t.Context()))
	}
	after := sessionReadState(t, f)
	require.Equal(t, before, after.DAGs[active.Ref.DAGID], "recovery must not claim expiry, refresh activity, or alter ordinary DAG metadata")
	require.Equal(t, active.Ref, after.Messages["101"])
	require.Equal(t, active.Ref, after.Runs[active.RunID])
	_, err = os.Stat(sessionArchivePath(f.dir, active))
	require.NoError(t, err)
	require.NotContains(t, after.DAGs, deleting.Ref.DAGID)
	for _, n := range []session.Node{deleting, pending.Node} {
		_, err = os.Stat(sessionArchivePath(f.dir, n))
		require.ErrorIs(t, err, os.ErrNotExist)
	}
	empty := after.DAGs[pending.Node.Ref.DAGID]
	require.NotNil(t, empty)
	require.Equal(t, "active", empty.State)
	require.Empty(t, empty.Intents)
	require.Empty(t, empty.Nodes)
	remaining, err := f.repo.Deleting(t.Context(), f.scope)
	require.NoError(t, err)
	require.Empty(t, remaining)
	// The same expired active DAGs are only claimed by the scheduled API.
	require.NoError(t, f.service.Collect(t.Context()))
	require.Empty(t, sessionReadState(t, f).DAGs)
	_, err = os.Stat(sessionArchivePath(f.dir, active))
	require.ErrorIs(t, err, os.ErrNotExist)
	require.NoError(t, f.service.Close())
	require.ErrorIs(t, f.service.Recover(t.Context()), session.ErrClosed)
}

func TestAgentV3SessionPositiveTTLAtMillisecondPrecision(t *testing.T) {
	for _, ttl := range []time.Duration{time.Nanosecond, time.Millisecond - time.Nanosecond, time.Millisecond} {
		t.Run(ttl.String(), func(t *testing.T) {
			f := newSessionFixture(t, session.Options{TTL: ttl})
			root, err := f.service.Commit(t.Context(), sessionRequest(t, f.scope, "A", nil, 101, "tiny TTL"))
			require.NoError(t, err)
			before := sessionReadState(t, f).DAGs[root.Ref.DAGID]
			require.NoError(t, f.service.Recover(t.Context()))
			require.Equal(t, before, sessionReadState(t, f).DAGs[root.Ref.DAGID])
			parent := fixtureLoad(t, f, f.service, 101)
			require.NoError(t, f.service.Collect(t.Context()))
			_, err = os.Stat(sessionArchivePath(f.dir, root))
			require.NoError(t, err, "tiny TTL must not bypass an active lease")
			require.NoError(t, parent.Parent.Close())
			if ttl >= time.Millisecond {
				f.mr.SetTime(f.now.Add(ttl))
			}
			// Sub-millisecond positive TTLs round down to zero milliseconds, so
			// no clock advance is needed to make this unpinned DAG eligible.
			require.NoError(t, f.service.Collect(t.Context()))
			_, err = os.Stat(sessionArchivePath(f.dir, root))
			require.ErrorIs(t, err, os.ErrNotExist)
			require.Empty(t, sessionReadState(t, f).DAGs)
		})
	}
}

func TestAgentV3SessionNonpositiveTTLAndSubmillisecondLeaseRejected(t *testing.T) {
	f := newSessionFixture(t, session.Options{})
	for _, ttl := range []time.Duration{0, -time.Nanosecond} {
		_, err := f.repo.ClaimDeleting(t.Context(), f.scope, ttl)
		require.ErrorIs(t, err, session.ErrCorrupt)
	}
	for _, lease := range []time.Duration{time.Nanosecond, time.Millisecond - time.Nanosecond, time.Millisecond} {
		files, err := session.NewFileStore(t.TempDir())
		require.NoError(t, err)
		svc, err := session.NewService(f.repo, files, session.Options{TTL: time.Nanosecond, LeaseDuration: lease})
		if lease < time.Millisecond {
			require.Error(t, err)
			require.NoError(t, files.Close())
		} else {
			require.NoError(t, err)
			require.NoError(t, svc.Close())
		}
	}
}

type sessionReleaseFault struct{ session.Repository }

func (*sessionReleaseFault) Release(context.Context, session.Scope, session.Lease) error {
	return errSessionReleaseFailure
}

func TestAgentV3SessionReleaseFailureIsReported(t *testing.T) {
	f := newSessionFixture(t, session.Options{})
	root, err := f.service.Commit(t.Context(), sessionRequest(t, f.scope, "A", nil, 101, "root"))
	require.NoError(t, err)
	svc := fixtureService(t, &sessionReleaseFault{Repository: f.repo}, f.dir, session.Options{})
	loaded := fixtureLoad(t, f, svc, 101)
	require.ErrorIs(t, loaded.Parent.Close(), errSessionReleaseFailure)
	require.ErrorIs(t, loaded.Parent.Close(), errSessionReleaseFailure)
	// A reported cleanup failure must not pretend the Redis pin was removed.
	state := sessionReadState(t, f)
	require.Len(t, state.DAGs[root.Ref.DAGID].Leases, 1)
	require.NoError(t, os.Remove(sessionArchivePath(f.dir, root)))
	broken, err := svc.Load(t.Context(), session.Selection{Scope: f.scope, Mode: session.SelectReply, ReplyMessageID: 101})
	require.ErrorIs(t, err, session.ErrCorrupt)
	require.ErrorIs(t, err, errSessionReleaseFailure)
	require.Nil(t, broken.Parent)
	require.Nil(t, broken.Messages)
}
