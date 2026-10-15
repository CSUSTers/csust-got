package session

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/require"
)

var errRenewTransient = errors.New("transient redis failure")

func TestServiceRenewLeaseRetriesTransientFailureDuringAcceptance(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		svc, repo, scope := newFakeLoadService(t, Options{LeaseDuration: 6 * time.Second, RenewInterval: time.Second})
		failures := 0
		repo.renewErr = func() error {
			failures++
			if failures == 1 {
				return errRenewTransient
			}
			return nil
		}
		loaded, err := svc.LoadWithAcceptance(t.Context(), Selection{Scope: scope, Mode: SelectLatest}, func(ctx context.Context, _ *LoadCandidate) error {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(3500 * time.Millisecond):
				return nil
			}
		})
		require.NoError(t, err, "one failed renewal must not reject a candidate whose lease is still valid")
		require.NotNil(t, loaded.Parent)
		t.Cleanup(func() { require.NoError(t, loaded.Parent.Close()) })
		repo.mu.Lock()
		defer repo.mu.Unlock()
		require.GreaterOrEqual(t, repo.renewed, 2)
		require.Equal(t, 1, repo.confirmed)
	})
}

func TestServiceRenewLeaseGivesUpBeforeLeaseExpiry(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		svc, repo, scope := newFakeLoadService(t, Options{LeaseDuration: 6 * time.Second, RenewInterval: time.Second})
		var attempts []time.Duration
		start := time.Now()
		repo.renewErr = func() error {
			attempts = append(attempts, time.Since(start))
			return errRenewTransient
		}
		loaded, err := svc.LoadWithAcceptance(t.Context(), Selection{Scope: scope, Mode: SelectLatest}, func(ctx context.Context, _ *LoadCandidate) error {
			<-ctx.Done()
			return nil
		})
		require.ErrorIs(t, err, errRenewTransient)
		require.Nil(t, loaded.Parent)
		require.GreaterOrEqual(t, len(attempts), 5, "renewal keeps retrying each tick while the lease is still valid")
		require.LessOrEqual(t, attempts[len(attempts)-1], 6*time.Second, "renewal stops before the lease would lapse")
		require.Zero(t, repo.confirmed)
		require.Equal(t, 1, repo.released)
	})
}

func TestServiceRenewLeaseFenceStopsImmediately(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		svc, repo, scope := newFakeLoadService(t, Options{LeaseDuration: 6 * time.Second, RenewInterval: time.Second})
		calls := 0
		repo.renewErr = func() error {
			calls++
			return ErrFence
		}
		_, err := svc.LoadWithAcceptance(t.Context(), Selection{Scope: scope, Mode: SelectLatest}, func(ctx context.Context, _ *LoadCandidate) error {
			<-ctx.Done()
			return nil
		})
		require.ErrorIs(t, err, ErrFence)
		require.Equal(t, 1, calls, "fencing is final and is not retried")
	})
}

func TestServiceLoadedParentSurvivesTransientRenewFailure(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		svc, repo, scope := newFakeLoadService(t, Options{LeaseDuration: 6 * time.Second, RenewInterval: time.Second})
		loaded, err := svc.Load(t.Context(), Selection{Scope: scope, Mode: SelectLatest})
		require.NoError(t, err)
		var failing atomic.Bool
		failing.Store(true)
		repo.setRenewErr(func() error {
			if failing.Load() {
				return errRenewTransient
			}
			return nil
		})
		time.Sleep(2500 * time.Millisecond)
		synctest.Wait()
		require.True(t, loaded.Parent.alive(), "the parent stays alive while renewal retries inside the lease")
		failing.Store(false)
		time.Sleep(2 * time.Second)
		synctest.Wait()
		require.True(t, loaded.Parent.alive())
		failing.Store(true)
		time.Sleep(7 * time.Second)
		synctest.Wait()
		require.False(t, loaded.Parent.alive(), "persistent failure past the lease duration invalidates the parent")
		require.NoError(t, loaded.Parent.Close())
	})
}
