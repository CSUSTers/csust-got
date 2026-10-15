package session

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestServiceMaintainScopeTakesLockPerDAGRemoval(t *testing.T) {
	repo := &maintenanceRepo{}
	repo.scopes = maintenanceScopes(repo)[:1]
	repo.pending = func(context.Context, Scope) ([]Intent, error) { return nil, nil }
	deletions := make([]Deletion, 0, 3)
	for range 3 {
		dag, err := NewID()
		require.NoError(t, err)
		generation, err := NewID()
		require.NoError(t, err)
		deletions = append(deletions, Deletion{DAGID: dag, Generation: generation})
	}
	repo.deleting = func(context.Context, Scope) ([]Deletion, error) { return deletions, nil }
	var finished []string
	repo.finishDelete = func(_ context.Context, _ Scope, deletion Deletion) error {
		finished = append(finished, deletion.DAGID)
		return nil
	}
	svc := maintenanceService(t, repo, time.Second)
	var locks []Scope
	svc.files.onLock = func(scope Scope) { locks = append(locks, scope) }
	require.NoError(t, svc.Recover(t.Context()))
	require.Len(t, locks, 2+len(deletions), "intent recovery, deletion discovery, and each DAG removal take their own lock")
	require.Len(t, finished, len(deletions))
	locks = nil
	require.NoError(t, svc.Collect(t.Context()))
	require.Len(t, locks, 2+len(deletions))
}

func TestServiceMaintainScopeWithoutDeletionsTakesTwoLocks(t *testing.T) {
	repo := &maintenanceRepo{}
	repo.scopes = maintenanceScopes(repo)
	repo.pending = func(context.Context, Scope) ([]Intent, error) { return nil, nil }
	svc := maintenanceService(t, repo, time.Second)
	count := 0
	svc.files.onLock = func(Scope) { count++ }
	require.NoError(t, svc.Recover(t.Context()))
	require.Equal(t, 2*len(repo.scopes), count)
}

func TestServiceCollectHasOwnScopeDeadline(t *testing.T) {
	repo := &maintenanceRepo{}
	repo.scopes = maintenanceScopes(repo)[:1]
	repo.pending = func(context.Context, Scope) ([]Intent, error) { return nil, nil }
	var budgets []time.Duration
	repo.deleting = func(ctx context.Context, _ Scope) ([]Deletion, error) {
		deadline, ok := ctx.Deadline()
		require.True(t, ok)
		budgets = append(budgets, time.Until(deadline))
		return nil, nil
	}
	files, err := NewFileStore(t.TempDir())
	require.NoError(t, err)
	svc, err := NewService(repo, files, Options{OperationTimeout: 50 * time.Millisecond, CollectTimeout: 30 * time.Second})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })
	require.NoError(t, svc.Recover(t.Context()))
	require.NoError(t, svc.Collect(t.Context()))
	require.Len(t, budgets, 2)
	require.LessOrEqual(t, budgets[0], 50*time.Millisecond, "recovery keeps the short operation budget")
	require.Greater(t, budgets[1], 10*time.Second, "collection uses its own longer budget")
}

func TestServiceOptionsCollectTimeoutDefaultsAndValidation(t *testing.T) {
	repo := &maintenanceRepo{}
	files, err := NewFileStore(t.TempDir())
	require.NoError(t, err)
	svc, err := NewService(repo, files, Options{})
	require.NoError(t, err)
	require.Equal(t, 2*time.Minute, svc.options.CollectTimeout)
	require.NoError(t, svc.Close())
	files, err = NewFileStore(t.TempDir())
	require.NoError(t, err)
	_, err = NewService(repo, files, Options{CollectTimeout: -time.Second})
	require.ErrorIs(t, err, errSessionDurations)
	require.NoError(t, files.Close())
}

type markerOnlyRepo struct {
	maintenanceRepo
	last  string
	marks []string
}

func (r *markerOnlyRepo) LastCollection(context.Context) (string, error) { return r.last, nil }
func (r *markerOnlyRepo) MarkCollection(_ context.Context, day string) error {
	r.last = day
	r.marks = append(r.marks, day)
	return nil
}

func TestServiceCollectionMarkerDelegatesToRepository(t *testing.T) {
	plain := &maintenanceRepo{}
	files, err := NewFileStore(t.TempDir())
	require.NoError(t, err)
	svc, err := NewService(plain, files, Options{})
	require.NoError(t, err)
	_, err = svc.LastCollection(t.Context())
	require.ErrorIs(t, err, errors.ErrUnsupported)
	require.ErrorIs(t, svc.MarkCollection(t.Context(), "2026-10-08"), errors.ErrUnsupported)
	require.NoError(t, svc.Close())

	marker := &markerOnlyRepo{}
	files, err = NewFileStore(t.TempDir())
	require.NoError(t, err)
	svc, err = NewService(marker, files, Options{})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })
	last, err := svc.LastCollection(t.Context())
	require.NoError(t, err)
	require.Empty(t, last)
	require.NoError(t, svc.MarkCollection(t.Context(), "2026-10-08"))
	last, err = svc.LastCollection(t.Context())
	require.NoError(t, err)
	require.Equal(t, "2026-10-08", last)
	require.Equal(t, []string{"2026-10-08"}, marker.marks)
}
