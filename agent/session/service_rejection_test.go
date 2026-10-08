package session

import (
	"context"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/require"
)

func TestServiceRejectContextForwardsScopeRefKeyAndBoundsIO(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		svc, repo, scope := newFakeLoadService(t, Options{})
		inputScope := scope
		inputScope.Namespace = ""
		repo.reject = func(ctx context.Context, got Scope, ref NodeRef, key string) error {
			require.Equal(t, scope, got)
			require.Equal(t, repo.node.Ref, ref)
			require.Equal(t, "agent:model:context", key)
			deadline, ok := ctx.Deadline()
			require.True(t, ok)
			require.Equal(t, 10*time.Second, time.Until(deadline))
			<-ctx.Done()
			return ctx.Err()
		}
		start := time.Now()
		require.ErrorIs(t, svc.RejectContext(t.Context(), inputScope, repo.node.Ref, "agent:model:context"), context.DeadlineExceeded)
		require.Equal(t, 10*time.Second, time.Since(start))
		require.Zero(t, repo.confirmed)
	})
}

func TestServiceRejectContextPropagatesRepositoryErrorAndScopeValidation(t *testing.T) {
	svc, repo, scope := newFakeLoadService(t, Options{})
	called := 0
	repo.reject = func(context.Context, Scope, NodeRef, string) error {
		called++
		return ErrFence
	}
	require.ErrorIs(t, svc.RejectContext(t.Context(), scope, repo.node.Ref, "key"), ErrFence)
	require.Equal(t, 1, called)
	scope.Namespace = StorageNamespace("another-deployment")
	require.ErrorIs(t, svc.RejectContext(t.Context(), scope, repo.node.Ref, "key"), ErrCorrupt)
	require.Equal(t, 1, called)
}

func TestServiceRejectContextCancellationAndCloseWaitForCleanup(t *testing.T) {
	for _, cause := range []string{"caller", "close"} {
		t.Run(cause, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				svc, repo, scope := newFakeLoadService(t, Options{})
				entered, canceled, cleanup := make(chan struct{}), make(chan struct{}), make(chan struct{})
				repo.reject = func(ctx context.Context, _ Scope, _ NodeRef, _ string) error {
					close(entered)
					<-ctx.Done()
					close(canceled)
					<-cleanup
					return nil // A late nil response must not erase caller/service cancellation.
				}
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				rejectDone := make(chan error, 1)
				go func() { rejectDone <- svc.RejectContext(ctx, scope, repo.node.Ref, "key") }()
				<-entered
				if cause == "caller" {
					cancel()
				}
				closed := make(chan error, 1)
				go func() { closed <- svc.Close() }()
				<-canceled
				synctest.Wait()
				select {
				case err := <-closed:
					t.Fatalf("Close detached RejectContext cleanup: %v", err)
				default:
				}
				close(cleanup)
				require.ErrorIs(t, <-rejectDone, context.Canceled)
				require.NoError(t, <-closed)
				require.ErrorIs(t, svc.RejectContext(t.Context(), scope, repo.node.Ref, "key"), ErrClosed)
			})
		})
	}
}

func TestServiceLoadPassesContextRejectionThroughResolve(t *testing.T) {
	svc, repo, scope := newFakeLoadService(t, Options{})
	repo.resolveErr = ErrContextRejected
	called := false
	loaded, err := svc.LoadWithAcceptance(t.Context(), Selection{Scope: scope, Mode: SelectLatest, ContextKey: "agent:model:context"}, func(context.Context, *LoadCandidate) error {
		called = true
		return nil
	})
	require.ErrorIs(t, err, ErrContextRejected)
	require.Equal(t, "agent:model:context", repo.selection.ContextKey)
	require.Nil(t, loaded.Parent)
	require.Nil(t, loaded.Messages)
	require.False(t, called)
	require.Zero(t, repo.confirmed)
	require.Zero(t, repo.released)
}
