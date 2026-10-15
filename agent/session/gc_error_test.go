package session

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"
	"github.com/stretchr/testify/require"
)

func maintenanceErrorArchive(t *testing.T, svc *Service, scope Scope) (Node, Deletion) {
	t.Helper()
	ids := make([]string, 4)
	for i := range ids {
		var err error
		ids[i], err = NewID()
		require.NoError(t, err)
	}
	node := Node{
		Scope: scope, Ref: NodeRef{DAGID: ids[0], NodeID: ids[1]}, RunID: ids[2],
		Agent: "agent", FileName: ids[1] + ".jsonl", Version: Version,
	}
	require.NoError(t, svc.files.WithScopeLock(t.Context(), scope, func(files *ScopeFiles) error {
		var err error
		node.Digest, node.Size, err = files.WriteAtomic(node, TurnCapture{
			Delta: History(schema.UserMessage("partial candidate"), schema.AssistantMessage("must not be removed on mixed error", nil)), Complete: true,
		})
		return err
	}))
	return node, Deletion{DAGID: ids[0], Generation: ids[3], Nodes: []Node{node}}
}

func TestServiceMaintenanceErrorClassification(t *testing.T) {
	errorsToTest := []struct {
		name string
		err  error
		pure bool
	}{
		{name: "corrupt-transport", err: errors.Join(ErrCorrupt, errMaintenanceTransport)},
		{name: "corrupt-conflict", err: errors.Join(ErrCorrupt, ErrConflict)},
		{name: "corrupt-canceled", err: errors.Join(ErrCorrupt, context.Canceled)},
		{name: "wrapped-mixed", err: fmt.Errorf("outer: %w", errors.Join(fmt.Errorf("archive: %w", ErrCorrupt), fmt.Errorf("transport: %w", errMaintenanceTransport)))},
		{name: "wrapped-corrupt", err: fmt.Errorf("outer: %w", fmt.Errorf("archive: %w", ErrCorrupt)), pure: true},
		{name: "wrapped-multi-corrupt", err: fmt.Errorf("outer: %w", errors.Join(fmt.Errorf("meta: %w", ErrCorrupt), fmt.Errorf("nodes: %w", errors.Join(ErrCorrupt, fmt.Errorf("lease: %w", ErrCorrupt))))), pure: true},
	}
	methods := []struct {
		name    string
		faultAt string
		collect bool
	}{
		{name: "catalog", faultAt: "scopes"},
		{name: "pending-recover", faultAt: "pending"},
		{name: "pending-collect", faultAt: "pending", collect: true},
		{name: "deleting", faultAt: "deleting"},
		{name: "claim", faultAt: "deleting", collect: true},
	}
	for _, method := range methods {
		for _, test := range errorsToTest {
			for _, withPartial := range []bool{false, true} {
				partialName := map[bool]string{false: "nil-partial", true: "file-partial"}[withPartial]
				t.Run(method.name+"/"+test.name+"/"+partialName, func(t *testing.T) {
					repo := &maintenanceRepo{}
					repo.scopes = maintenanceScopes(repo)
					svc := maintenanceService(t, repo, time.Second)
					node, deletion := maintenanceErrorArchive(t, svc, repo.scopes[0])
					var partialIntents []Intent
					var partialDeletions []Deletion
					if withPartial {
						partialIntents = []Intent{{Node: node, Status: "pending"}}
						partialDeletions = []Deletion{deletion}
					}
					var visited []Scope
					var badDeletionReads, badAborts, badFinishes, badDeletes, healthyFinishes int
					repo.catalog = func(context.Context) ([]Scope, error) {
						if method.faultAt != "scopes" {
							return repo.scopes, nil
						}
						if withPartial {
							return repo.scopes, test.err
						}
						return nil, test.err
					}
					repo.pending = func(_ context.Context, scope Scope) ([]Intent, error) {
						visited = append(visited, scope)
						if scope == repo.scopes[1] {
							return []Intent{{Status: "published"}}, nil
						}
						if method.faultAt == "pending" {
							return partialIntents, test.err
						}
						if method.faultAt == "scopes" {
							return partialIntents, nil
						}
						return nil, nil
					}
					repo.abort = func(ctx context.Context, _ Scope, _ Intent) (bool, error) {
						require.NoError(t, ctx.Err())
						badAborts++
						return true, nil
					}
					repo.finish = func(_ context.Context, scope Scope, _ Intent) error {
						if scope == repo.scopes[0] {
							badFinishes++
						} else {
							healthyFinishes++
						}
						return nil
					}
					repo.deleting = func(_ context.Context, scope Scope) ([]Deletion, error) {
						if scope == repo.scopes[1] {
							return nil, nil
						}
						badDeletionReads++
						if method.faultAt == "deleting" {
							return partialDeletions, test.err
						}
						return nil, nil
					}
					repo.finishDelete = func(context.Context, Scope, Deletion) error {
						badDeletes++
						return nil
					}
					err := svc.maintain(t.Context(), method.collect)
					require.ErrorIs(t, err, test.err)
					if method.faultAt == "scopes" && (!test.pure || !withPartial) {
						require.Empty(t, visited, "catalog failure must not authorize any returned scopes")
						require.Zero(t, healthyFinishes)
						require.Zero(t, badDeletionReads)
					} else {
						require.Equal(t, repo.scopes, visited, "an independent healthy scope must still run")
						require.Equal(t, 1, healthyFinishes)
						if method.faultAt == "pending" && !test.pure {
							require.Zero(t, badDeletionReads, "failed Pending must not start Deleting or ClaimDeleting")
						} else {
							require.Equal(t, 1, badDeletionReads)
						}
					}
					if test.pure && withPartial {
						if method.faultAt == "deleting" {
							require.Equal(t, 1, badDeletes)
							require.Zero(t, badAborts)
							require.Zero(t, badFinishes)
						} else {
							require.Equal(t, 1, badAborts)
							require.Equal(t, 1, badFinishes)
							require.Zero(t, badDeletes)
						}
					} else {
						require.Zero(t, badAborts, "untrusted partial intent must not be aborted")
						require.Zero(t, badFinishes)
						require.Zero(t, badDeletes, "untrusted partial manifest must not be finalized")
					}
					require.NoError(t, svc.files.WithScopeLock(t.Context(), node.Scope, func(files *ScopeFiles) error {
						_, err := files.Read(node)
						if test.pure && withPartial {
							require.ErrorIs(t, err, fs.ErrNotExist)
						} else {
							require.NoError(t, err, "mixed errors must retain the partial candidate's file")
						}
						return nil
					}))
				})
			}
		}
	}
}
