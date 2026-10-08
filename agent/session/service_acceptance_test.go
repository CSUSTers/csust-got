package session_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"csust-got/agent/session"
	"csust-got/orm"

	"github.com/alicebob/miniredis/v2"
	"github.com/cloudwego/eino/schema"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

const acceptancePrefix = "acceptance-test:"

var (
	errAcceptanceRejected = errors.New("candidate rejected")
	errAcceptanceRelease  = errors.New("candidate release failed")
)

type acceptanceRepo struct {
	session.Repository
	confirmed  atomic.Int64
	released   atomic.Int64
	releaseErr error
	confirm    func(context.Context, session.Scope, session.Lease, time.Duration) error
}

func (r *acceptanceRepo) ConfirmLoaded(ctx context.Context, scope session.Scope, lease session.Lease, duration time.Duration) error {
	r.confirmed.Add(1)
	if r.confirm != nil {
		return r.confirm(ctx, scope, lease, duration)
	}
	return r.Repository.ConfirmLoaded(ctx, scope, lease, duration)
}

func (r *acceptanceRepo) Release(ctx context.Context, scope session.Scope, lease session.Lease) error {
	r.released.Add(1)
	if r.releaseErr != nil {
		return r.releaseErr
	}
	return r.Repository.Release(ctx, scope, lease)
}

type acceptanceFixture struct {
	redis   *miniredis.Miniredis
	client  *redis.Client
	repo    *acceptanceRepo
	service *session.Service
	files   *session.FileStore
	dir     string
	scope   session.Scope
	now     time.Time
}

func newAcceptanceFixture(t *testing.T) *acceptanceFixture {
	t.Helper()
	mr := miniredis.RunT(t)
	now := time.Date(2026, 10, 8, 10, 0, 0, 0, time.UTC)
	mr.SetTime(now)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr(), MaxRetries: -1})
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	repo, err := orm.NewAgentV3SessionRepository(client, acceptancePrefix)
	require.NoError(t, err)
	fault := &acceptanceRepo{Repository: repo}
	dir := t.TempDir()
	files, err := session.NewFileStore(dir)
	require.NoError(t, err)
	svc, err := session.NewService(fault, files, session.Options{LeaseDuration: time.Hour, RenewInterval: 30 * time.Minute, OperationTimeout: time.Second})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })
	return &acceptanceFixture{redis: mr, client: client, repo: fault, service: svc, files: files, dir: dir, scope: session.Scope{Namespace: repo.Namespace(), Bot: "bot", Platform: "telegram", ChatID: 1}, now: now}
}

func acceptanceID(t *testing.T) string {
	t.Helper()
	id, err := session.NewID()
	require.NoError(t, err)
	return id
}

func (f *acceptanceFixture) commit(t *testing.T, scope session.Scope, parent *session.LoadedParent, id int, text string) session.Node {
	t.Helper()
	n, err := f.service.Commit(t.Context(), session.CommitRequest{
		Scope: scope, Agent: "agent", RunID: acceptanceID(t), Parent: parent,
		Capture: session.TurnCapture{Delta: session.History(schema.UserMessage(text), schema.AssistantMessage("answer "+text, nil)), Complete: true},
		Receipt: session.DeliveryReceipt{MessageIDs: []int{id}},
	})
	require.NoError(t, err)
	return n
}

func (f *acceptanceFixture) selection(id int) session.Selection {
	return session.Selection{Scope: f.scope, Mode: session.SelectReply, ReplyMessageID: id}
}

type acceptanceDAGState struct {
	Generation string                   `json:"generation"`
	LastActive int64                    `json:"last_active"`
	Leases     map[string]session.Lease `json:"-"`
}

func (f *acceptanceFixture) scopePrefix() string {
	return acceptancePrefix + "agentv3:session:{" + f.scope.Namespace + "}:scope:" + f.scope.Key() + ":"
}

func (f *acceptanceFixture) dagKey(id, part string) string {
	return f.scopePrefix() + "dag:" + id + ":" + part
}

func (f *acceptanceFixture) dag(t *testing.T, id string) acceptanceDAGState {
	t.Helper()
	data, err := f.client.Get(t.Context(), f.dagKey(id, "meta")).Bytes()
	require.NoError(t, err)
	var dag acceptanceDAGState
	require.NoError(t, json.Unmarshal(data, &dag))
	rawLeases, err := f.client.HGetAll(t.Context(), f.dagKey(id, "leases")).Result()
	require.NoError(t, err)
	dag.Leases = make(map[string]session.Lease, len(rawLeases))
	for token, raw := range rawLeases {
		var lease session.Lease
		require.NoError(t, json.Unmarshal([]byte(raw), &lease))
		dag.Leases[token] = lease
	}
	return dag
}

func TestServiceAcceptanceRejectDoesNotConfirm(t *testing.T) {
	f := newAcceptanceFixture(t)
	root := f.commit(t, f.scope, nil, 101, "root")
	before := f.dag(t, root.Ref.DAGID).LastActive
	f.redis.SetTime(f.now.Add(time.Minute))
	loaded, err := f.service.LoadWithAcceptance(t.Context(), f.selection(101), func(_ context.Context, candidate *session.LoadCandidate) error {
		require.Len(t, candidate.Messages, 2)
		require.True(t, candidate.ContainsReplyMessageID(101))
		return errAcceptanceRejected
	})
	require.ErrorIs(t, err, errAcceptanceRejected)
	require.Nil(t, loaded.Parent)
	require.Nil(t, loaded.Messages)
	require.Zero(t, f.repo.confirmed.Load(), "rejected candidate must never refresh activity")
	require.EqualValues(t, 1, f.repo.released.Load())
	require.Equal(t, before, f.dag(t, root.Ref.DAGID).LastActive)
	require.Empty(t, f.dag(t, root.Ref.DAGID).Leases)
	fallback := f.commit(t, f.scope, loaded.Parent, 102, "fallback without candidate")
	require.Nil(t, fallback.Parent)
	require.NotEqual(t, root.Ref.DAGID, fallback.Ref.DAGID)
}

func TestServiceAcceptanceReleaseFailureIsJoined(t *testing.T) {
	f := newAcceptanceFixture(t)
	root := f.commit(t, f.scope, nil, 101, "root")
	f.repo.releaseErr = errAcceptanceRelease
	loaded, err := f.service.LoadWithAcceptance(t.Context(), f.selection(101), func(context.Context, *session.LoadCandidate) error {
		return errAcceptanceRejected
	})
	require.ErrorIs(t, err, errAcceptanceRejected)
	require.ErrorIs(t, err, errAcceptanceRelease)
	require.Nil(t, loaded.Parent)
	require.Nil(t, loaded.Messages)
	require.Zero(t, f.repo.confirmed.Load())
	require.EqualValues(t, 1, f.repo.released.Load())
	for _, lease := range f.dag(t, root.Ref.DAGID).Leases {
		require.NoError(t, f.repo.Repository.Release(t.Context(), f.scope, lease))
	}
}

func TestServiceAcceptanceDoesNotHoldScopeLock(t *testing.T) {
	f := newAcceptanceFixture(t)
	f.commit(t, f.scope, nil, 101, "root")
	loaded, err := f.service.LoadWithAcceptance(t.Context(), f.selection(101), func(ctx context.Context, _ *session.LoadCandidate) error {
		lockCtx, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
		defer cancel()
		return f.files.WithScopeLock(lockCtx, f.scope, func(*session.ScopeFiles) error { return nil })
	})
	require.NoError(t, err)
	require.NoError(t, loaded.Parent.Close())
	require.EqualValues(t, 1, f.repo.confirmed.Load())
}

func TestServiceAcceptanceCloseCancelsAndWaitsForRelease(t *testing.T) {
	f := newAcceptanceFixture(t)
	root := f.commit(t, f.scope, nil, 101, "root")
	before := f.dag(t, root.Ref.DAGID).LastActive
	entered := make(chan struct{})
	loadDone := make(chan error, 1)
	go func() {
		loaded, err := f.service.LoadWithAcceptance(t.Context(), f.selection(101), func(ctx context.Context, _ *session.LoadCandidate) error {
			close(entered)
			<-ctx.Done()
			return ctx.Err()
		})
		if loaded.Parent != nil || loaded.Messages != nil {
			err = errors.Join(err, errAcceptanceRejected)
		}
		loadDone <- err
	}()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("acceptance did not start")
	}
	closeDone := make(chan error, 1)
	go func() { closeDone <- f.service.Close() }()
	select {
	case err := <-closeDone:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not wait for the tracked callback and release")
	}
	require.ErrorIs(t, <-loadDone, context.Canceled)
	require.Empty(t, f.dag(t, root.Ref.DAGID).Leases)
	require.Equal(t, before, f.dag(t, root.Ref.DAGID).LastActive)
	require.Zero(t, f.repo.confirmed.Load())
	require.EqualValues(t, 1, f.repo.released.Load())
	_, err := f.service.Load(t.Context(), f.selection(101))
	require.ErrorIs(t, err, session.ErrClosed)
}

func TestServiceAcceptanceCancellationAndDeadline(t *testing.T) {
	for _, cause := range []string{"caller", "deadline", "confirm-race"} {
		t.Run(cause, func(t *testing.T) {
			f := newAcceptanceFixture(t)
			root := f.commit(t, f.scope, nil, 101, "root")
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if cause == "deadline" {
				var deadlineCancel context.CancelFunc
				ctx, deadlineCancel = context.WithTimeout(ctx, time.Second)
				defer deadlineCancel()
			}
			if cause == "confirm-race" {
				f.repo.confirm = func(ctx context.Context, scope session.Scope, lease session.Lease, duration time.Duration) error {
					err := f.repo.Repository.ConfirmLoaded(ctx, scope, lease, duration)
					cancel()
					return err
				}
			}
			loaded, err := f.service.LoadWithAcceptance(ctx, f.selection(101), func(ctx context.Context, _ *session.LoadCandidate) error {
				if cause == "caller" {
					cancel()
				}
				if cause == "deadline" {
					<-ctx.Done()
				}
				return nil // The service must still check cancellation even if accept ignores it.
			})
			if cause == "deadline" {
				require.ErrorIs(t, err, context.DeadlineExceeded)
			} else {
				require.ErrorIs(t, err, context.Canceled)
			}
			require.Nil(t, loaded.Parent)
			require.Nil(t, loaded.Messages)
			require.Empty(t, f.dag(t, root.Ref.DAGID).Leases)
			require.EqualValues(t, 1, f.repo.released.Load())
			if cause != "confirm-race" {
				require.Zero(t, f.repo.confirmed.Load())
			}
		})
	}
}

func TestServiceAcceptanceCloseDuringConfirmationReleasesPin(t *testing.T) {
	f := newAcceptanceFixture(t)
	root := f.commit(t, f.scope, nil, 101, "root")
	confirmed := make(chan struct{})
	f.repo.confirm = func(ctx context.Context, scope session.Scope, lease session.Lease, duration time.Duration) error {
		if err := f.repo.Repository.ConfirmLoaded(ctx, scope, lease, duration); err != nil {
			return err
		}
		close(confirmed)
		<-ctx.Done()
		return nil
	}
	loadDone := make(chan error, 1)
	go func() {
		loaded, err := f.service.LoadWithAcceptance(t.Context(), f.selection(101), nil)
		if loaded.Parent != nil || loaded.Messages != nil {
			err = errors.Join(err, errAcceptanceRejected)
		}
		loadDone <- err
	}()
	select {
	case <-confirmed:
	case <-time.After(2 * time.Second):
		t.Fatal("confirmation did not start")
	}
	closeDone := make(chan error, 1)
	go func() { closeDone <- f.service.Close() }()
	select {
	case err := <-closeDone:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("Close raced past a tracked confirmation")
	}
	require.ErrorIs(t, <-loadDone, context.Canceled)
	require.Empty(t, f.dag(t, root.Ref.DAGID).Leases)
	require.EqualValues(t, 1, f.repo.released.Load())
}

func TestServiceCloseReportsRegisteredParentReleaseFailure(t *testing.T) {
	f := newAcceptanceFixture(t)
	root := f.commit(t, f.scope, nil, 101, "root")
	fault := &acceptanceRepo{Repository: f.repo.Repository, releaseErr: errAcceptanceRelease}
	files, err := session.NewFileStore(f.dir)
	require.NoError(t, err)
	svc, err := session.NewService(fault, files, session.Options{})
	require.NoError(t, err)
	t.Cleanup(func() { require.ErrorIs(t, svc.Close(), errAcceptanceRelease) })
	loaded, err := svc.Load(t.Context(), f.selection(101))
	require.NoError(t, err)
	require.ErrorIs(t, svc.Close(), errAcceptanceRelease)
	require.ErrorIs(t, loaded.Parent.Close(), errAcceptanceRelease)
	for _, lease := range f.dag(t, root.Ref.DAGID).Leases {
		require.NoError(t, f.repo.Repository.Release(t.Context(), f.scope, lease))
	}
}

func TestServiceAcceptanceRejectsChangedFenceAndExpiredLease(t *testing.T) {
	for _, cause := range []string{"generation", "expired", "released"} {
		t.Run(cause, func(t *testing.T) {
			f := newAcceptanceFixture(t)
			root := f.commit(t, f.scope, nil, 101, "root")
			before := f.dag(t, root.Ref.DAGID).LastActive
			loaded, err := f.service.LoadWithAcceptance(t.Context(), f.selection(101), func(context.Context, *session.LoadCandidate) error {
				switch cause {
				case "generation":
					key := f.dagKey(root.Ref.DAGID, "meta")
					data, err := f.client.Get(t.Context(), key).Bytes()
					require.NoError(t, err)
					var meta map[string]json.RawMessage
					require.NoError(t, json.Unmarshal(data, &meta))
					meta["generation"], err = json.Marshal(acceptanceID(t))
					require.NoError(t, err)
					data, err = json.Marshal(meta)
					require.NoError(t, err)
					_, err = f.client.TxPipelined(t.Context(), func(pipe redis.Pipeliner) error {
						pipe.Set(t.Context(), key, data, 0)
						pipe.Del(t.Context(), f.dagKey(root.Ref.DAGID, "leases"))
						return nil
					})
					return err
				case "expired":
					f.redis.SetTime(f.now.Add(2 * time.Hour))
				case "released":
					for _, lease := range f.dag(t, root.Ref.DAGID).Leases {
						require.NoError(t, f.repo.Repository.Release(t.Context(), f.scope, lease))
					}
				}
				return nil
			})
			require.ErrorIs(t, err, session.ErrFence)
			require.Nil(t, loaded.Parent)
			require.Nil(t, loaded.Messages)
			require.Equal(t, before, f.dag(t, root.Ref.DAGID).LastActive)
			require.Empty(t, f.dag(t, root.Ref.DAGID).Leases)
			require.EqualValues(t, 1, f.repo.confirmed.Load())
			require.EqualValues(t, 1, f.repo.released.Load())
		})
	}
}

func TestServiceAcceptanceAndOrdinaryLoadPreserveActivityAndReuse(t *testing.T) {
	f := newAcceptanceFixture(t)
	root := f.commit(t, f.scope, nil, 101, "root")
	for i, gated := range []bool{true, false} {
		f.redis.SetTime(f.now.Add(time.Duration(i+1) * time.Minute))
		var loaded session.LoadResult
		var err error
		if gated {
			loaded, err = f.service.LoadWithAcceptance(t.Context(), f.selection(101), func(_ context.Context, candidate *session.LoadCandidate) error {
				require.Equal(t, "root", candidate.Messages[0].Content)
				return nil
			})
		} else {
			loaded, err = f.service.Load(t.Context(), f.selection(101))
		}
		require.NoError(t, err)
		require.Equal(t, root.Ref, loaded.Parent.Ref())
		require.True(t, loaded.Parent.ContainsReplyMessageID(101))
		require.Equal(t, f.now.Add(time.Duration(i+1)*time.Minute).UnixMilli(), f.dag(t, root.Ref.DAGID).LastActive)
		// No Commit: save=false/load=true still refreshes activity, then releases its pin.
		require.NoError(t, loaded.Parent.Close())
		require.Empty(t, f.dag(t, root.Ref.DAGID).Leases)
	}
	loaded, err := f.service.Load(t.Context(), f.selection(101))
	require.NoError(t, err)
	child := f.commit(t, f.scope, loaded.Parent, 102, "child")
	require.Equal(t, root.Ref, *child.Parent)
	require.NoError(t, loaded.Parent.Close())
}

func TestServiceAcceptanceProofOnlyCoversSelectedAncestors(t *testing.T) {
	f := newAcceptanceFixture(t)
	f.commit(t, f.scope, nil, 101, "root")
	root, err := f.service.Load(t.Context(), f.selection(101))
	require.NoError(t, err)
	f.commit(t, f.scope, root.Parent, 102, "selected branch")
	f.commit(t, f.scope, root.Parent, 103, "sibling branch")
	require.NoError(t, root.Parent.Close())
	other := f.scope
	other.ChatID++
	f.commit(t, other, nil, 104, "other scope")
	loaded, err := f.service.LoadWithAcceptance(t.Context(), f.selection(102), func(_ context.Context, candidate *session.LoadCandidate) error {
		require.True(t, candidate.ContainsReplyMessageID(101))
		require.True(t, candidate.ContainsReplyMessageID(102))
		for _, id := range []int{-1, 0, 103, 104} {
			require.False(t, candidate.ContainsReplyMessageID(id))
		}
		require.Len(t, candidate.Messages, 4)
		return nil
	})
	require.NoError(t, err)
	defer loaded.Parent.Close()
	require.True(t, loaded.Parent.ContainsReplyMessageID(101))
	require.True(t, loaded.Parent.ContainsReplyMessageID(102))
	require.False(t, loaded.Parent.ContainsReplyMessageID(103))
	require.False(t, loaded.Parent.ContainsReplyMessageID(104))
	_, err = f.service.Commit(t.Context(), session.CommitRequest{
		Scope: other, Agent: "agent", RunID: acceptanceID(t), Parent: loaded.Parent,
		Capture: session.TurnCapture{Delta: session.History(schema.UserMessage("cross scope")), Complete: true},
		Receipt: session.DeliveryReceipt{MessageIDs: []int{105}},
	})
	require.ErrorIs(t, err, session.ErrCorrupt)
}

func TestServiceFencedParentNewRootsKeepPrivateHistoryDuringPublicMutationAndConcurrentCapture(t *testing.T) {
	f := newAcceptanceFixture(t)
	var input schema.Message
	require.NoError(t, json.Unmarshal([]byte(`{"role":"user","content":"root","user_input_multi_content":[{"type":"image_url","image":{"base64data":"original media","mime_type":"image/png"}}],"extra":{"nested":{"list":["original"],"integer":9007199254740993}}}`), &input))
	input.Extra["nested"].(map[string]any)["integer"] = json.Number("9007199254740993")
	root, err := f.service.Commit(t.Context(), session.CommitRequest{
		Scope: f.scope, Agent: "agent", RunID: acceptanceID(t),
		Capture: session.TurnCapture{
			Bootstrap: session.History(schema.UserMessage("bootstrap"), schema.AssistantMessage("bootstrap answer", nil)),
			Delta:     session.History(&input, schema.AssistantMessage("root answer", nil)), Complete: true,
		},
		Receipt: session.DeliveryReceipt{MessageIDs: []int{101}},
	})
	require.NoError(t, err)
	rootLoad, err := f.service.Load(t.Context(), f.selection(101))
	require.NoError(t, err)
	child := f.commit(t, f.scope, rootLoad.Parent, 102, "child")
	require.NoError(t, rootLoad.Parent.Close())
	var original []byte
	loaded, err := f.service.LoadWithAcceptance(t.Context(), f.selection(102), func(_ context.Context, candidate *session.LoadCandidate) error {
		original, err = json.Marshal(candidate.Messages)
		require.NoError(t, err)
		candidate.Messages[0].Content = "callback mutation"
		*candidate.Messages[2].UserInputMultiContent[0].Image.Base64Data = "callback media mutation"
		candidate.Messages[2].Extra["nested"].(map[string]any)["list"].([]any)[0] = "callback nested mutation"
		return nil
	})
	require.NoError(t, err)
	defer loaded.Parent.Close()
	require.Equal(t, child.Ref, loaded.Parent.Ref())
	for _, lease := range f.dag(t, root.Ref.DAGID).Leases {
		require.NoError(t, f.repo.Repository.Release(t.Context(), f.scope, lease))
	}
	started, stop, mutationDone := make(chan struct{}), make(chan struct{}), make(chan struct{})
	go func() {
		defer close(mutationDone)
		close(started)
		for {
			select {
			case <-stop:
				return
			default:
				loaded.Messages[0].Content = "caller mutation"
				*loaded.Messages[2].UserInputMultiContent[0].Image.Base64Data = "caller media mutation"
				loaded.Messages[2].Extra["nested"].(map[string]any)["list"].([]any)[0] = "caller nested mutation"
			}
		}
	}()
	<-started
	var workers sync.WaitGroup
	var nodes [2]session.Node
	var failures [2]error
	for i := range nodes {
		id := acceptanceID(t)
		workers.Go(func() {
			nodes[i], failures[i] = f.service.Commit(t.Context(), session.CommitRequest{
				Scope: f.scope, Agent: "agent", RunID: id, Parent: loaded.Parent,
				Capture: session.TurnCapture{Delta: session.History(schema.UserMessage("new turn"), schema.AssistantMessage("new answer", nil)), Complete: true},
				Receipt: session.DeliveryReceipt{MessageIDs: []int{103 + i}},
			})
		})
	}
	workers.Wait()
	close(stop)
	<-mutationDone
	for i, node := range nodes {
		require.NoError(t, failures[i])
		require.Nil(t, node.Parent)
		require.NotEqual(t, root.Ref.DAGID, node.Ref.DAGID)
		fresh, err := f.service.Load(t.Context(), f.selection(103+i))
		require.NoError(t, err)
		require.Len(t, fresh.Messages, 8, "new roots need bootstrap + all ancestors + their complete new turn")
		got, err := json.Marshal(fresh.Messages[:6])
		require.NoError(t, err)
		require.Equal(t, original, got, "private full-history fallback must survive callback and caller mutations")
		require.NoError(t, fresh.Parent.Close())
	}
}

func TestServiceRejectedContextUsesRepositorySelectionGate(t *testing.T) {
	f := newAcceptanceFixture(t)
	root := f.commit(t, f.scope, nil, 101, "root")
	before := f.dag(t, root.Ref.DAGID).LastActive
	f.redis.SetTime(f.now.Add(time.Minute))
	require.NoError(t, f.service.RejectContext(t.Context(), f.scope, root.Ref, "agent:model-small"))
	require.Equal(t, before, f.dag(t, root.Ref.DAGID).LastActive)
	require.Empty(t, f.dag(t, root.Ref.DAGID).Leases)
	selection := f.selection(101)
	selection.ContextKey = "agent:model-small"
	loaded, err := f.service.LoadWithAcceptance(t.Context(), selection, func(context.Context, *session.LoadCandidate) error {
		t.Error("a repository-rejected context must not reach acceptance")
		return nil
	})
	require.ErrorIs(t, err, session.ErrContextRejected)
	require.Nil(t, loaded.Parent)
	require.Nil(t, loaded.Messages)
	require.Zero(t, f.repo.confirmed.Load())
	require.Zero(t, f.repo.released.Load())
	require.Equal(t, before, f.dag(t, root.Ref.DAGID).LastActive)
	selection.ContextKey = "agent:model-large"
	selection.LoadOnly = true
	loaded, err = f.service.Load(t.Context(), selection)
	require.NoError(t, err)
	require.Equal(t, root.Ref, loaded.Parent.Ref())
	require.Equal(t, f.now.Add(time.Minute).UnixMilli(), f.dag(t, root.Ref.DAGID).LastActive)
	require.NoError(t, loaded.Parent.Close())
	require.Empty(t, f.dag(t, root.Ref.DAGID).Leases)
}
