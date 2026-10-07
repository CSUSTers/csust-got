package session

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type maintenanceRepo struct {
	Repository
	scopes       []Scope
	scopesErr    error
	catalog      func(context.Context) ([]Scope, error)
	pending      func(context.Context, Scope) ([]Intent, error)
	abort        func(context.Context, Scope, Intent) (bool, error)
	finish       func(context.Context, Scope, Intent) error
	deleting     func(context.Context, Scope) ([]Deletion, error)
	finishDelete func(context.Context, Scope, Deletion) error
}

func (*maintenanceRepo) Namespace() string { return StorageNamespace("maintenance-test") }

func (r *maintenanceRepo) Scopes(ctx context.Context) ([]Scope, error) {
	if r.catalog != nil {
		return r.catalog(ctx)
	}
	return r.scopes, r.scopesErr
}

func TestServiceMaintenanceCatalogHasIndependentDeadline(t *testing.T) {
	repo := &maintenanceRepo{}
	repo.catalog = func(ctx context.Context) ([]Scope, error) {
		_, ok := ctx.Deadline()
		require.True(t, ok)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	svc := maintenanceService(t, repo, 50*time.Millisecond)
	require.ErrorIs(t, svc.Recover(t.Context()), context.DeadlineExceeded)
}

func TestServiceMaintenanceCloseCancelsActiveScan(t *testing.T) {
	repo := &maintenanceRepo{}
	repo.scopes = maintenanceScopes(repo)
	entered := make(chan struct{})
	var visited []Scope
	repo.pending = func(ctx context.Context, scope Scope) ([]Intent, error) {
		visited = append(visited, scope)
		close(entered)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	svc := maintenanceService(t, repo, time.Second)
	recoverDone := make(chan error, 1)
	go func() { recoverDone <- svc.Recover(t.Context()) }()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("scope maintenance did not start")
	}
	closeDone := make(chan error, 1)
	go func() { closeDone <- svc.Close() }()
	select {
	case err := <-closeDone:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not cancel and wait for maintenance")
	}
	require.ErrorIs(t, <-recoverDone, context.Canceled)
	require.Equal(t, repo.scopes[:1], visited)
}

func (r *maintenanceRepo) Pending(ctx context.Context, scope Scope) ([]Intent, error) {
	return r.pending(ctx, scope)
}

func (r *maintenanceRepo) AbortIntent(ctx context.Context, scope Scope, intent Intent) (bool, error) {
	return r.abort(ctx, scope, intent)
}

func (r *maintenanceRepo) FinishIntent(ctx context.Context, scope Scope, intent Intent) error {
	return r.finish(ctx, scope, intent)
}

func (r *maintenanceRepo) Deleting(ctx context.Context, scope Scope) ([]Deletion, error) {
	if r.deleting != nil {
		return r.deleting(ctx, scope)
	}
	return nil, nil
}

func (r *maintenanceRepo) ClaimDeleting(ctx context.Context, scope Scope, _ time.Duration) ([]Deletion, error) {
	return r.Deleting(ctx, scope)
}

func (r *maintenanceRepo) FinishDelete(ctx context.Context, scope Scope, deletion Deletion) error {
	return r.finishDelete(ctx, scope, deletion)
}

func maintenanceService(t *testing.T, repo *maintenanceRepo, timeout time.Duration) *Service {
	t.Helper()
	files, err := NewFileStore(t.TempDir())
	require.NoError(t, err)
	svc, err := NewService(repo, files, Options{OperationTimeout: timeout})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })
	return svc
}

func maintenanceScopes(repo *maintenanceRepo) []Scope {
	return []Scope{
		{Namespace: repo.Namespace(), Bot: "bot", Platform: "telegram", ChatID: 1},
		{Namespace: repo.Namespace(), Bot: "bot", Platform: "telegram", ChatID: 2},
	}
}

func TestServiceMaintenancePartialScopesContinue(t *testing.T) {
	for _, collect := range []bool{false, true} {
		t.Run(map[bool]string{false: "recover", true: "collect"}[collect], func(t *testing.T) {
			repo := &maintenanceRepo{scopesErr: ErrCorrupt}
			repo.scopes = maintenanceScopes(repo)
			var visited []Scope
			repo.pending = func(_ context.Context, scope Scope) ([]Intent, error) {
				visited = append(visited, scope)
				return nil, nil
			}
			svc := maintenanceService(t, repo, time.Second)
			err := svc.maintain(t.Context(), collect)
			require.ErrorIs(t, err, ErrCorrupt)
			require.Equal(t, repo.scopes, visited)
		})
	}
}

func TestServiceMaintenanceFreshScopeBudget(t *testing.T) {
	repo := &maintenanceRepo{}
	repo.scopes = maintenanceScopes(repo)
	var visited []Scope
	repo.pending = func(ctx context.Context, scope Scope) ([]Intent, error) {
		visited = append(visited, scope)
		if scope == repo.scopes[0] {
			<-ctx.Done()
			return nil, ctx.Err()
		}
		require.NoError(t, ctx.Err(), "slow first scope must not consume next scope's budget")
		deadline, ok := ctx.Deadline()
		require.True(t, ok)
		require.Greater(t, time.Until(deadline), 50*time.Millisecond)
		return nil, nil
	}
	svc := maintenanceService(t, repo, 200*time.Millisecond)
	require.ErrorIs(t, svc.Recover(t.Context()), context.DeadlineExceeded)
	require.Equal(t, repo.scopes, visited)
}

func TestServiceMaintenanceCancellationStopsPending(t *testing.T) {
	for _, status := range []string{"pending", "published"} {
		t.Run(status, func(t *testing.T) {
			repo := &maintenanceRepo{}
			repo.scopes = maintenanceScopes(repo)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var visited, aborted, finished, deleting int
			repo.pending = func(context.Context, Scope) ([]Intent, error) {
				visited++
				return []Intent{{Status: status}, {Status: status}, {Status: status}}, nil
			}
			repo.abort = func(cleanup context.Context, _ Scope, _ Intent) (bool, error) {
				aborted++
				require.NoError(t, cleanup.Err())
				cancel()
				return false, nil
			}
			repo.finish = func(context.Context, Scope, Intent) error {
				finished++
				cancel()
				return nil
			}
			repo.deleting = func(context.Context, Scope) ([]Deletion, error) {
				deleting++
				return nil, nil
			}
			svc := maintenanceService(t, repo, time.Second)
			err := svc.Recover(ctx)
			if status == "pending" {
				require.Equal(t, 1, aborted)
				require.Zero(t, finished)
			} else {
				require.Equal(t, 1, finished)
				require.Zero(t, aborted)
			}
			require.ErrorIs(t, err, context.Canceled)
			require.Equal(t, 1, visited)
			require.Zero(t, deleting)
		})
	}
}

func TestServiceMaintenancePartialDAGResultsContinue(t *testing.T) {
	for _, method := range []string{"pending", "deleting", "claim", "pending-and-deleting"} {
		t.Run(method, func(t *testing.T) {
			repo := &maintenanceRepo{}
			repo.scopes = maintenanceScopes(repo)[:1]
			var finishedIntents, finishedDeletions int
			repo.pending = func(context.Context, Scope) ([]Intent, error) {
				var err error
				if method == "pending" || method == "pending-and-deleting" {
					err = errors.Join(fmt.Errorf("%w: pending bad DAG", ErrCorrupt))
				}
				return []Intent{{Status: "published"}}, err
			}
			repo.finish = func(ctx context.Context, _ Scope, _ Intent) error {
				require.NoError(t, ctx.Err())
				finishedIntents++
				return nil
			}
			dagID, err := NewID()
			require.NoError(t, err)
			generation, err := NewID()
			require.NoError(t, err)
			repo.deleting = func(context.Context, Scope) ([]Deletion, error) {
				var err error
				if method != "pending" {
					err = errors.Join(fmt.Errorf("%w: deleting bad DAG", ErrCorrupt))
				}
				return []Deletion{{DAGID: dagID, Generation: generation}}, err
			}
			repo.finishDelete = func(ctx context.Context, _ Scope, deletion Deletion) error {
				require.NoError(t, ctx.Err())
				require.Equal(t, dagID, deletion.DAGID)
				finishedDeletions++
				return nil
			}
			svc := maintenanceService(t, repo, time.Second)
			err = svc.maintain(t.Context(), method == "claim")
			require.ErrorIs(t, err, ErrCorrupt)
			require.Equal(t, 1, finishedIntents)
			require.Equal(t, 1, finishedDeletions)
			if method == "pending-and-deleting" {
				require.ErrorContains(t, err, "pending bad DAG")
				require.ErrorContains(t, err, "deleting bad DAG")
			}
		})
	}
}

var errMaintenanceTransport = errors.New("maintenance transport failure")

func TestServiceMaintenanceListFailureStopsScope(t *testing.T) {
	for _, method := range []string{"pending", "deleting", "claim"} {
		t.Run(method, func(t *testing.T) {
			repo := &maintenanceRepo{}
			repo.scopes = maintenanceScopes(repo)
			var visited, finished []Scope
			var deletionReads int
			repo.pending = func(_ context.Context, scope Scope) ([]Intent, error) {
				visited = append(visited, scope)
				if scope == repo.scopes[0] && method == "pending" {
					return nil, errMaintenanceTransport
				}
				return []Intent{{Status: "published"}}, nil
			}
			repo.finish = func(_ context.Context, scope Scope, _ Intent) error {
				finished = append(finished, scope)
				return nil
			}
			repo.deleting = func(_ context.Context, scope Scope) ([]Deletion, error) {
				deletionReads++
				if scope == repo.scopes[0] {
					return nil, errMaintenanceTransport
				}
				return nil, nil
			}
			svc := maintenanceService(t, repo, time.Second)
			require.ErrorIs(t, svc.maintain(t.Context(), method == "claim"), errMaintenanceTransport)
			require.Equal(t, repo.scopes, visited)
			if method == "pending" {
				require.Equal(t, repo.scopes[1:], finished)
				require.Equal(t, 1, deletionReads)
			} else {
				require.Equal(t, repo.scopes, finished)
				require.Equal(t, 2, deletionReads)
			}
		})
	}
}

func TestServiceMaintenanceCanceledPartialResultsDoNotStartWork(t *testing.T) {
	for _, method := range []string{"pending", "deleting", "claim"} {
		t.Run(method, func(t *testing.T) {
			repo := &maintenanceRepo{}
			repo.scopes = maintenanceScopes(repo)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var pendingReads int
			repo.pending = func(context.Context, Scope) ([]Intent, error) {
				pendingReads++
				if method == "pending" {
					cancel()
					return []Intent{{Status: "published"}}, ErrCorrupt
				}
				return nil, nil
			}
			repo.deleting = func(context.Context, Scope) ([]Deletion, error) {
				cancel()
				return []Deletion{{}}, ErrCorrupt
			}
			repo.finish = func(context.Context, Scope, Intent) error {
				t.Error("canceled partial results must not finish an intent")
				return nil
			}
			repo.finishDelete = func(context.Context, Scope, Deletion) error {
				t.Error("canceled partial results must not finish a deletion")
				return nil
			}
			svc := maintenanceService(t, repo, time.Second)
			err := svc.maintain(ctx, method == "claim")
			require.ErrorIs(t, err, context.Canceled)
			require.ErrorIs(t, err, ErrCorrupt)
			require.Equal(t, 1, pendingReads)
		})
	}
}

func TestServiceMaintenanceLockedScopeDoesNotStarveNext(t *testing.T) {
	repo := &maintenanceRepo{}
	repo.scopes = maintenanceScopes(repo)
	var visited []Scope
	repo.pending = func(ctx context.Context, scope Scope) ([]Intent, error) {
		require.NoError(t, ctx.Err())
		visited = append(visited, scope)
		return nil, nil
	}
	svc := maintenanceService(t, repo, 200*time.Millisecond)
	locked := make(chan struct{})
	unlockScope := make(chan struct{})
	lockDone := make(chan error, 1)
	go func() {
		lockDone <- svc.files.WithScopeLock(t.Context(), repo.scopes[0], func(*ScopeFiles) error {
			close(locked)
			<-unlockScope
			return nil
		})
	}()
	defer func() { close(unlockScope); require.NoError(t, <-lockDone) }()
	select {
	case <-locked:
	case <-time.After(2 * time.Second):
		t.Fatal("first scope lock was not acquired")
	}
	require.ErrorIs(t, svc.Recover(t.Context()), context.DeadlineExceeded)
	require.Equal(t, repo.scopes[1:], visited)
}
