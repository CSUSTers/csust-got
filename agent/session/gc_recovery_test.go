package session_test

import (
	"context"
	"testing"
	"time"

	"csust-got/agent/session"

	"github.com/cloudwego/eino/schema"
	"github.com/stretchr/testify/require"
)

type cancellationRepo struct {
	session.Repository
	cancel     context.CancelFunc
	aborts     int
	finishes   int
	deletes    int
	cancelRead bool
}

func (r *cancellationRepo) AbortIntent(ctx context.Context, scope session.Scope, intent session.Intent) (bool, error) {
	r.aborts++
	ok, err := r.Repository.AbortIntent(ctx, scope, intent)
	r.cancel()
	return ok, err
}

func (r *cancellationRepo) FinishIntent(ctx context.Context, scope session.Scope, intent session.Intent) error {
	r.finishes++
	return r.Repository.FinishIntent(ctx, scope, intent)
}

func (r *cancellationRepo) Deleting(ctx context.Context, scope session.Scope) ([]session.Deletion, error) {
	deletions, err := r.Repository.Deleting(ctx, scope)
	if r.cancelRead {
		r.cancel()
	}
	return deletions, err
}

func (r *cancellationRepo) FinishDelete(ctx context.Context, scope session.Scope, deletion session.Deletion) error {
	r.deletes++
	err := r.Repository.FinishDelete(ctx, scope, deletion)
	r.cancel()
	return err
}

func recoveryService(t *testing.T, repo session.Repository, files *session.FileStore) *session.Service {
	t.Helper()
	svc, err := session.NewService(repo, files, session.Options{OperationTimeout: time.Second})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })
	return svc
}

func TestServiceRecoveryCancellationFinishesOnlyStartedAbort(t *testing.T) {
	f := newAcceptanceFixture(t)
	var intents []session.Intent
	require.NoError(t, f.files.WithScopeLock(t.Context(), f.scope, func(files *session.ScopeFiles) error {
		for range 3 {
			intent, err := f.repo.Reserve(t.Context(), session.Reservation{Scope: f.scope, Agent: "agent", RunID: acceptanceID(t)}, time.Minute)
			if err != nil {
				return err
			}
			intent.Node.Digest, intent.Node.Size, err = files.WriteAtomic(intent.Node, session.TurnCapture{Delta: session.History(schema.UserMessage("unpublished"), schema.AssistantMessage("unpublished answer", nil)), Complete: true})
			if err != nil {
				return err
			}
			intents = append(intents, intent)
		}
		return nil
	}))
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	fault := &cancellationRepo{Repository: f.repo, cancel: cancel}
	// Open a second owned root handle: the two services must not share Close ownership.
	files, err := session.NewFileStore(f.dir)
	require.NoError(t, err)
	svc := recoveryService(t, fault, files)
	require.ErrorIs(t, svc.Recover(ctx), context.Canceled)
	require.Equal(t, 1, fault.aborts)
	require.Equal(t, 1, fault.finishes)
	pending, err := f.repo.Pending(t.Context(), f.scope)
	require.NoError(t, err)
	require.Len(t, pending, 2)
	pendingRuns := map[string]bool{}
	for _, intent := range pending {
		pendingRuns[intent.Node.RunID] = true
	}
	require.NoError(t, f.files.WithScopeLock(t.Context(), f.scope, func(files *session.ScopeFiles) error {
		for _, intent := range intents {
			if pendingRuns[intent.Node.RunID] {
				_, err := files.Read(intent.Node)
				require.NoError(t, err)
			}
		}
		return nil
	}))
	require.NoError(t, f.service.Recover(t.Context()))
	pending, err = f.repo.Pending(t.Context(), f.scope)
	require.NoError(t, err)
	require.Empty(t, pending)
}

func TestServiceRecoveryCancellationRetainsUnstartedDeletions(t *testing.T) {
	for _, cancelRead := range []bool{true, false} {
		t.Run(map[bool]string{true: "before-files", false: "between-dags"}[cancelRead], func(t *testing.T) {
			f := newAcceptanceFixture(t)
			for i := range 3 {
				f.commit(t, f.scope, nil, 101+i, "expired root")
			}
			f.redis.SetTime(f.now.Add(2 * time.Hour))
			deletions, err := f.repo.ClaimDeleting(t.Context(), f.scope, time.Minute)
			require.NoError(t, err)
			require.Len(t, deletions, 3)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			fault := &cancellationRepo{Repository: f.repo, cancel: cancel, cancelRead: cancelRead}
			files, err := session.NewFileStore(f.dir)
			require.NoError(t, err)
			svc := recoveryService(t, fault, files)
			require.ErrorIs(t, svc.Recover(ctx), context.Canceled)
			remaining, err := f.repo.Deleting(t.Context(), f.scope)
			require.NoError(t, err)
			if cancelRead {
				require.Zero(t, fault.deletes)
				require.Len(t, remaining, 3)
			} else {
				require.Equal(t, 1, fault.deletes)
				require.Len(t, remaining, 2)
			}
			require.NoError(t, f.files.WithScopeLock(t.Context(), f.scope, func(files *session.ScopeFiles) error {
				for _, deletion := range remaining {
					for _, node := range deletion.Nodes {
						_, err := files.Read(node)
						require.NoError(t, err)
					}
				}
				return nil
			}))
			require.NoError(t, f.service.Recover(t.Context()))
			remaining, err = f.repo.Deleting(t.Context(), f.scope)
			require.NoError(t, err)
			require.Empty(t, remaining)
		})
	}
}
