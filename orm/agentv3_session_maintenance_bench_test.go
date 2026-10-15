package orm

import (
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"csust-got/agent/session"

	"github.com/stretchr/testify/require"
)

func sessionPublishedIntentFixture(state sessionState) {
	for _, dag := range state.DAGs {
		for _, node := range dag.Nodes {
			lease := session.Lease{DAGID: dag.ID, Generation: dag.Generation, Token: node.RunID, Deadline: dag.LastActive + time.Hour.Milliseconds()}
			dag.Intents[node.RunID] = session.Intent{Node: node, Lease: lease, Status: sessionIntentPublished}
		}
	}
}

func sessionWriteMaintenanceArchive(t testing.TB, files *session.FileStore, node session.Node, capture session.TurnCapture) {
	t.Helper()
	require.NoError(t, files.WithScopeLock(t.Context(), node.Scope, func(fs *session.ScopeFiles) error {
		digest, size, err := fs.WriteAtomic(node, capture)
		if err != nil {
			return err
		}
		if node.Digest != "" {
			require.Equal(t, node.Digest, digest)
			require.Equal(t, node.Size, size)
		}
		return nil
	}))
}

func BenchmarkAgentV3SessionMaintenance(b *testing.B) { benchmarkSessionMaintenance(b, nil) }

func benchmarkSessionMaintenance(b *testing.B, clockSetup func(testing.TB, *sessionFixture)) {
	for _, roots := range []int{1, 100, 1000} {
		b.Run(fmt.Sprintf("roots%d", roots), func(b *testing.B) {
			f := newSessionFixture(b, session.Options{TTL: 7 * 24 * time.Hour, LeaseDuration: time.Hour, RenewInterval: 30 * time.Minute})
			if clockSetup != nil {
				clockSetup(b, f)
			}
			base := sessionPartitionFixture(b, f, "roots", roots)
			files, err := session.NewFileStore(f.dir)
			require.NoError(b, err)
			b.Cleanup(func() { require.NoError(b, files.Close()) })
			counter := sessionCountCommands(b, f)
			for _, operation := range []string{"CollectIdle", "RecoverIdle", "CollectOneDue", "RecoverMixed"} {
				b.Run(operation, func(b *testing.B) {
					state := sessionBenchmarkState(f.scope, base.DAGs[fmt.Sprintf("%032x", 1)].LastActive, "roots", roots)
					for id, dag := range state.DAGs {
						for field := range dag.Nodes {
							dag.Nodes[field] = base.DAGs[id].Nodes[field]
						}
					}
					due := state.DAGs[fmt.Sprintf("%032x", 1)]
					if operation == "CollectOneDue" {
						due.LastActive -= 8 * 24 * time.Hour.Milliseconds()
					}
					if operation == "RecoverMixed" {
						sessionPublishedIntentFixture(state)
					}
					totals := newSessionStats()
					var removedBytes int64
					b.ReportAllocs()
					b.ResetTimer()
					for range b.N {
						b.StopTimer()
						sessionRestoreFixture(b, f, state)
						sessionAdvanceFixture(b, f)
						if operation == "CollectOneDue" {
							for _, node := range due.Nodes {
								_, err := os.Stat(sessionArchivePath(f.dir, node))
								if errors.Is(err, os.ErrNotExist) {
									sessionWriteMaintenanceArchive(b, files, node, sessionBenchmarkCapture())
								} else {
									require.NoError(b, err)
								}
							}
						}
						var pending []session.Intent
						if operation == "RecoverMixed" {
							for range min(3, roots) {
								i, err := f.repo.Reserve(b.Context(), session.Reservation{Scope: f.scope, Agent: "A", RunID: sessionRun(b)}, time.Hour)
								require.NoError(b, err)
								sessionWriteMaintenanceArchive(b, files, i.Node, sessionBenchmarkCapture())
								pending = append(pending, i)
							}
						}
						counter.reset()
						b.StartTimer()
						if operation == "CollectIdle" || operation == "CollectOneDue" {
							err = f.service.Collect(b.Context())
						} else {
							err = f.service.Recover(b.Context())
						}
						b.StopTimer()
						require.NoError(b, err)
						s := counter.snapshot()
						for k, n := range s.commands {
							totals.commands[k] += n
						}
						for k, n := range s.readValues {
							totals.readValues[k] += n
						}
						for k, n := range s.writeValues {
							totals.writeValues[k] += n
						}
						totals.logical += s.logical
						totals.wire += s.wire
						totals.wireTime += s.wireTime
						totals.pipelines += s.pipelines
						totals.conflicts += s.conflicts
						view := sessionReadState(b, f)
						if operation == "CollectOneDue" {
							require.NotContains(b, view.DAGs, due.ID)
							for _, node := range due.Nodes {
								_, err := os.Stat(sessionArchivePath(f.dir, node))
								require.ErrorIs(b, err, os.ErrNotExist)
								removedBytes += node.Size
							}
							require.Len(b, view.DAGs, roots-1)
						} else {
							for id, dag := range state.DAGs {
								require.Equal(b, dag.Nodes, view.DAGs[id].Nodes)
							}
						}
						for _, i := range pending {
							require.Empty(b, view.DAGs[i.Node.Ref.DAGID].Intents)
							_, err := os.Stat(sessionArchivePath(f.dir, i.Node))
							require.ErrorIs(b, err, os.ErrNotExist)
						}
						if operation == "RecoverMixed" {
							for _, dag := range view.DAGs {
								require.Empty(b, dag.Intents)
							}
						}
					}
					b.ReportMetric(float64(removedBytes)/float64(b.N), "removed-JSONL-B/op")
					totals.report(b)
				})
			}
		})
	}
}
