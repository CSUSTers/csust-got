package session

import (
	"context"
	"errors"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/cloudwego/eino/schema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeRepository struct {
	Repository
	mu         sync.Mutex
	node       Node
	lease      Lease
	lastActive time.Time
	renewed    int
	confirmed  int
	released   int
	selection  Selection
	resolveErr error
	resolve    func(context.Context)
	confirm    func(context.Context)
	renew      func(context.Context)
	release    func(context.Context)
	reject     func(context.Context, Scope, NodeRef, string) error
}

func (*fakeRepository) Namespace() string { return StorageNamespace("service-load-test") }

func (r *fakeRepository) ResolveAndPin(ctx context.Context, selection Selection, token string, duration time.Duration) (Pinned, error) {
	if r.resolve != nil {
		r.resolve(ctx)
	}
	if err := ctx.Err(); err != nil {
		return Pinned{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.selection = selection
	if r.resolveErr != nil {
		return Pinned{}, r.resolveErr
	}
	r.lease = Lease{DAGID: r.node.Ref.DAGID, Generation: r.node.Ref.DAGID, Token: token, Deadline: time.Now().Add(duration).UnixMilli()}
	return Pinned{Nodes: []Node{r.node}, Lease: r.lease}, nil
}

func (r *fakeRepository) RejectContext(ctx context.Context, scope Scope, ref NodeRef, key string) error {
	return r.reject(ctx, scope, ref, key)
}

func (r *fakeRepository) valid(lease Lease) bool {
	return r.lease.Token == lease.Token && r.lease.Generation == lease.Generation && r.lease.DAGID == lease.DAGID && r.lease.Deadline > time.Now().UnixMilli()
}

func (r *fakeRepository) Renew(ctx context.Context, _ Scope, lease Lease, duration time.Duration) error {
	if r.renew != nil {
		r.renew(ctx)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if !r.valid(lease) {
		return ErrFence
	}
	r.renewed++
	r.lease.Deadline = time.Now().Add(duration).UnixMilli()
	return nil
}

func (r *fakeRepository) ConfirmLoaded(ctx context.Context, _ Scope, lease Lease, duration time.Duration) error {
	if r.confirm != nil {
		r.confirm(ctx)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.confirmed++
	if err := ctx.Err(); err != nil {
		return err
	}
	if !r.valid(lease) {
		return ErrFence
	}
	r.lastActive = time.Now()
	r.lease.Deadline = time.Now().Add(duration).UnixMilli()
	return nil
}

func (r *fakeRepository) Release(ctx context.Context, _ Scope, lease Lease) error {
	if r.release != nil {
		r.release(ctx)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	r.released++
	if r.lease.Token == lease.Token {
		r.lease = Lease{}
	}
	return nil
}

func newFakeLoadService(t *testing.T, options Options) (*Service, *fakeRepository, Scope) {
	t.Helper()
	repo := &fakeRepository{}
	scope := Scope{Namespace: repo.Namespace(), Bot: "bot", Platform: "telegram", ChatID: 1}
	id := func() string {
		id, err := NewID()
		require.NoError(t, err)
		return id
	}
	repo.node = Node{Scope: scope, Ref: NodeRef{DAGID: id(), NodeID: id()}, Agent: "agent", RunID: id(), Version: Version}
	repo.node.FileName = repo.node.Ref.NodeID + ".jsonl"
	files, err := NewFileStore(t.TempDir())
	require.NoError(t, err)
	require.NoError(t, files.WithScopeLock(t.Context(), scope, func(files *ScopeFiles) error {
		var err error
		repo.node.Digest, repo.node.Size, err = files.WriteAtomic(repo.node, TurnCapture{
			Delta: History(schema.UserMessage("root"), schema.AssistantMessage("answer", nil)), Complete: true,
		})
		return err
	}))
	svc, err := NewService(repo, files, options)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })
	return svc, repo, scope
}

func TestServiceSlowAcceptanceHasIndependentBudgetAndRenewedPin(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		svc, repo, scope := newFakeLoadService(t, Options{LeaseDuration: 6 * time.Second, RenewInterval: time.Second})
		repo.resolve = func(ctx context.Context) {
			deadline, ok := ctx.Deadline()
			require.True(t, ok)
			require.Equal(t, 10*time.Second, time.Until(deadline))
			time.Sleep(2 * time.Second)
		}
		repo.confirm = func(ctx context.Context) {
			deadline, ok := ctx.Deadline()
			require.True(t, ok)
			require.Equal(t, 10*time.Second, time.Until(deadline), "confirmation must receive a fresh IO budget")
		}
		loaded, err := svc.LoadWithAcceptance(t.Context(), Selection{Scope: scope, Mode: SelectLatest}, func(ctx context.Context, _ *LoadCandidate) error {
			_, hasDeadline := ctx.Deadline()
			assert.False(t, hasDeadline, "callback must not inherit the read phase deadline")
			require.NoError(t, svc.files.WithScopeLock(ctx, scope, func(*ScopeFiles) error { return nil }))
			other := scope
			other.ChatID++
			require.NoError(t, svc.files.WithScopeLock(ctx, other, func(*ScopeFiles) error { return nil }))
			time.Sleep(12 * time.Second)
			synctest.Wait()
			repo.mu.Lock()
			renewed, confirmed, active := repo.renewed, repo.confirmed, repo.lastActive
			repo.mu.Unlock()
			assert.Positive(t, renewed, "pin must renew while acceptance is running")
			require.Zero(t, confirmed)
			require.Zero(t, active)
			return ctx.Err()
		})
		require.NoError(t, err)
		require.NotNil(t, loaded.Parent)
		require.Len(t, loaded.Messages, 2)
		require.NoError(t, loaded.Parent.Close())
		require.Equal(t, 1, repo.confirmed)
		require.Equal(t, 1, repo.released)
		require.Empty(t, repo.lease.Token)
	})
}

func TestServiceSlowAcceptanceCancellationAndClose(t *testing.T) {
	for _, cause := range []string{"caller", "deadline", "close"} {
		t.Run(cause, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				svc, repo, scope := newFakeLoadService(t, Options{LeaseDuration: 6 * time.Second, RenewInterval: time.Second})
				ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
				defer cancel()
				entered := make(chan struct{})
				type outcome struct {
					result LoadResult
					err    error
				}
				finished := make(chan outcome, 1)
				go func() {
					result, err := svc.LoadWithAcceptance(ctx, Selection{Scope: scope, Mode: SelectLatest}, func(ctx context.Context, _ *LoadCandidate) error {
						deadline, ok := ctx.Deadline()
						require.True(t, ok)
						require.Equal(t, 20*time.Second, time.Until(deadline))
						close(entered)
						<-ctx.Done()
						return nil
					})
					finished <- outcome{result: result, err: err}
				}()
				<-entered
				time.Sleep(12 * time.Second)
				synctest.Wait()
				select {
				case out := <-finished:
					t.Fatalf("load ended before caller/service cancellation: %v", out.err)
				default:
				}
				switch cause {
				case "caller":
					cancel()
				case "deadline":
					time.Sleep(8 * time.Second)
				case "close":
					require.NoError(t, svc.Close())
				}
				out := <-finished
				expected := context.Canceled
				if cause == "deadline" {
					expected = context.DeadlineExceeded
				}
				require.ErrorIs(t, out.err, expected)
				require.Nil(t, out.result.Parent)
				require.Nil(t, out.result.Messages)
				require.Zero(t, repo.confirmed)
				require.Zero(t, repo.lastActive)
				require.Equal(t, 1, repo.released)
				require.Empty(t, repo.lease.Token)
			})
		})
	}
}

func TestServiceSlowAcceptanceRenewFailureRejectsCandidate(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		svc, repo, scope := newFakeLoadService(t, Options{LeaseDuration: 6 * time.Second, RenewInterval: time.Second})
		repo.renew = func(context.Context) {
			repo.mu.Lock()
			repo.lease.Generation = "changed generation"
			repo.mu.Unlock()
		}
		loaded, err := svc.LoadWithAcceptance(t.Context(), Selection{Scope: scope, Mode: SelectLatest}, func(ctx context.Context, _ *LoadCandidate) error {
			<-ctx.Done()
			return nil
		})
		require.ErrorIs(t, err, ErrFence)
		require.Nil(t, loaded.Parent)
		require.Nil(t, loaded.Messages)
		require.Zero(t, repo.confirmed)
		require.Zero(t, repo.lastActive)
		require.Equal(t, 1, repo.released)
	})
}

func TestServiceCloseWaitsForOpaqueAcceptanceAndCleanup(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		svc, repo, scope := newFakeLoadService(t, Options{LeaseDuration: 6 * time.Second, RenewInterval: time.Second})
		entered, finishCPU := make(chan struct{}), make(chan struct{})
		loadDone := make(chan error, 1)
		go func() {
			_, err := svc.LoadWithAcceptance(t.Context(), Selection{Scope: scope, Mode: SelectLatest}, func(context.Context, *LoadCandidate) error {
				close(entered)
				<-finishCPU // Models an opaque phase that does not cooperate with cancellation.
				return nil
			})
			loadDone <- err
		}()
		<-entered
		closed := make(chan error, 1)
		go func() { closed <- svc.Close() }()
		synctest.Wait()
		select {
		case err := <-closed:
			t.Fatalf("Close detached a non-cooperative callback: %v", err)
		default:
		}
		close(finishCPU)
		require.NoError(t, <-closed)
		err := <-loadDone
		require.True(t, errors.Is(err, context.Canceled) || errors.Is(err, ErrClosed))
		require.Equal(t, 1, repo.released)
		require.Zero(t, repo.confirmed)
	})
}

func TestServiceLoadStoragePhasesRemainBounded(t *testing.T) {
	for _, phase := range []string{"read", "confirm"} {
		t.Run(phase, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				svc, repo, scope := newFakeLoadService(t, Options{})
				blocked := func(ctx context.Context) { <-ctx.Done() }
				if phase == "read" {
					repo.resolve = blocked
				} else {
					repo.confirm = blocked
				}
				repo.release = func(ctx context.Context) {
					deadline, ok := ctx.Deadline()
					require.True(t, ok)
					require.Equal(t, 10*time.Second, time.Until(deadline))
					require.NoError(t, ctx.Err(), "cleanup must not reuse the failed storage phase context")
				}
				start := time.Now()
				called := false
				loaded, err := svc.LoadWithAcceptance(t.Context(), Selection{Scope: scope, Mode: SelectLatest}, func(context.Context, *LoadCandidate) error {
					called = true
					return nil
				})
				require.ErrorIs(t, err, context.DeadlineExceeded)
				require.Equal(t, 10*time.Second, time.Since(start))
				require.Nil(t, loaded.Parent)
				require.Nil(t, loaded.Messages)
				require.Zero(t, repo.lastActive)
				require.Equal(t, phase == "confirm", called)
				if phase == "confirm" {
					require.Equal(t, 1, repo.released)
				}
			})
		})
	}
}

func TestServiceLoadReadAndConfirmLockWaitsAreBounded(t *testing.T) {
	for _, phase := range []string{"read", "confirm"} {
		t.Run(phase, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				svc, repo, scope := newFakeLoadService(t, Options{})
				locked, unlock, lockDone := make(chan struct{}), make(chan struct{}), make(chan error, 1)
				holdLock := func() {
					go func() {
						lockDone <- svc.files.WithScopeLock(t.Context(), scope, func(*ScopeFiles) error {
							close(locked)
							<-unlock
							return nil
						})
					}()
					<-locked
				}
				if phase == "read" {
					holdLock()
				}
				loadDone := make(chan error, 1)
				go func() {
					loaded, err := svc.LoadWithAcceptance(t.Context(), Selection{Scope: scope, Mode: SelectLatest}, func(context.Context, *LoadCandidate) error {
						holdLock()
						return nil
					})
					assert.Nil(t, loaded.Parent)
					assert.Nil(t, loaded.Messages)
					loadDone <- err
				}()
				synctest.Wait()
				time.Sleep(10 * time.Second)
				synctest.Wait()
				// A confirm failure still awaits its fresh, bounded release, which needs this lock.
				close(unlock)
				require.NoError(t, <-lockDone)
				require.ErrorIs(t, <-loadDone, context.DeadlineExceeded)
				require.Zero(t, repo.confirmed)
				require.Zero(t, repo.lastActive)
			})
		})
	}
}

func TestServiceLoadReadAndConfirmationHoldScopeLock(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		svc, repo, scope := newFakeLoadService(t, Options{})
		checkLocked := func(ctx context.Context) {
			lockCtx, cancel := context.WithTimeout(ctx, time.Second)
			defer cancel()
			err := svc.files.WithScopeLock(lockCtx, scope, func(*ScopeFiles) error {
				t.Error("storage phase released the scope lock before its repository operation")
				return nil
			})
			require.ErrorIs(t, err, context.DeadlineExceeded)
		}
		repo.resolve, repo.confirm = checkLocked, checkLocked
		loaded, err := svc.LoadWithAcceptance(t.Context(), Selection{Scope: scope, Mode: SelectLatest}, func(ctx context.Context, _ *LoadCandidate) error {
			return svc.files.WithScopeLock(ctx, scope, func(*ScopeFiles) error { return nil })
		})
		require.NoError(t, err)
		require.NoError(t, loaded.Parent.Close())
	})
}

func TestServiceCloseWaitsForCandidateRenewalCleanup(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		svc, repo, scope := newFakeLoadService(t, Options{LeaseDuration: 6 * time.Second, RenewInterval: time.Second})
		entered, renewEntered, cleanup := make(chan struct{}), make(chan struct{}), make(chan struct{})
		repo.renew = func(ctx context.Context) {
			close(renewEntered)
			<-ctx.Done()
			<-cleanup
		}
		loadDone := make(chan error, 1)
		go func() {
			loaded, err := svc.LoadWithAcceptance(t.Context(), Selection{Scope: scope, Mode: SelectLatest}, func(ctx context.Context, _ *LoadCandidate) error {
				close(entered)
				<-ctx.Done()
				return nil
			})
			assert.Nil(t, loaded.Parent)
			assert.Nil(t, loaded.Messages)
			loadDone <- err
		}()
		<-entered
		time.Sleep(time.Second)
		<-renewEntered
		closeDone := make(chan error, 1)
		go func() { closeDone <- svc.Close() }()
		synctest.Wait()
		select {
		case err := <-closeDone:
			t.Fatalf("Close detached renewal cleanup: %v", err)
		default:
		}
		require.Zero(t, repo.released)
		close(cleanup)
		require.NoError(t, <-closeDone)
		require.ErrorIs(t, <-loadDone, context.Canceled)
		require.Equal(t, 1, repo.released)
		require.Zero(t, repo.confirmed)
	})
}

func TestServiceRejectedLoadCleanupHasFreshBudget(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		svc, repo, scope := newFakeLoadService(t, Options{})
		repo.release = func(ctx context.Context) {
			deadline, ok := ctx.Deadline()
			require.True(t, ok)
			require.Equal(t, 10*time.Second, time.Until(deadline))
			require.NoError(t, ctx.Err())
			time.Sleep(time.Second)
		}
		loaded, err := svc.LoadWithAcceptance(t.Context(), Selection{Scope: scope, Mode: SelectLatest}, func(context.Context, *LoadCandidate) error {
			time.Sleep(12 * time.Second)
			return ErrContextRejected
		})
		require.ErrorIs(t, err, ErrContextRejected)
		require.Nil(t, loaded.Parent)
		require.Nil(t, loaded.Messages)
		require.Zero(t, repo.confirmed)
		require.Zero(t, repo.lastActive)
		require.Equal(t, 1, repo.released)
	})
}
