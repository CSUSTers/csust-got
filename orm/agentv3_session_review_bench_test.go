package orm

import (
	"fmt"
	"os"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"csust-got/agent/session"

	"github.com/stretchr/testify/require"
)

func BenchmarkAgentV3SessionMultiScopeRoots(b *testing.B) {
	f := newSessionFixture(b, session.Options{})
	scopes := make([]session.Scope, runtime.GOMAXPROCS(0))
	for i := range scopes {
		scopes[i] = f.scope
		scopes[i].ChatID += int64(i)
		_, err := f.repo.Reserve(b.Context(), session.Reservation{Scope: scopes[i], Agent: "A", RunID: sessionRun(b)}, time.Hour)
		require.NoError(b, err)
	}
	counter := sessionCountCommands(b, f)
	var workers, success atomic.Int64
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		// Each worker exclusively owns one pre-registered scope.
		scope := scopes[int(workers.Add(1)-1)]
		for pb.Next() {
			run, err := session.NewID()
			if err == nil {
				_, err = f.repo.Reserve(b.Context(), session.Reservation{Scope: scope, Agent: "A", RunID: run}, time.Hour)
			}
			if err != nil {
				b.Error(err)
			} else {
				success.Add(1)
			}
		}
	})
	b.StopTimer()
	stats := counter.snapshot()
	stats.report(b)
	b.ReportMetric(float64(stats.writeValues["catalog"])/float64(b.N), "catalog-HSET-B/op")
	require.EqualValues(b, b.N, success.Load())
}

func BenchmarkAgentV3SessionIndexedRecovery(b *testing.B) {
	for _, roots := range []int{1, 100, 1000} {
		b.Run(fmt.Sprintf("roots%d", roots), func(b *testing.B) {
			for _, operation := range []string{"Empty", "PendingDeleting"} {
				b.Run(operation, func(b *testing.B) {
					f := newSessionFixture(b, session.Options{})
					base := sessionPartitionFixture(b, f, "roots", roots)
					files, err := session.NewFileStore(f.dir)
					require.NoError(b, err)
					b.Cleanup(func() { require.NoError(b, files.Close()) })
					counter := sessionCountCommands(b, f)
					totals := newSessionStats()
					b.ReportAllocs()
					b.ResetTimer()
					for range b.N {
						b.StopTimer()
						sessionRestoreFixture(b, f, base)
						var published, pending, due session.Intent
						if operation == "PendingDeleting" {
							// Leave one published intent for FinishIntent, one unpublished
							// archive for compensation, and one real deleting tombstone.
							published, err = f.repo.Reserve(b.Context(), session.Reservation{Scope: f.scope, Agent: "A", RunID: sessionRun(b)}, time.Hour)
							require.NoError(b, err)
							require.NoError(b, files.WithScopeLock(b.Context(), f.scope, func(fs *session.ScopeFiles) error {
								digest, size, err := fs.WriteAtomic(published.Node, sessionBenchmarkCapture())
								if err == nil {
									_, err = f.repo.Publish(b.Context(), f.scope, published, digest, size, session.DeliveryReceipt{MessageIDs: []int{10001}})
								}
								return err
							}))
							pending, err = f.repo.Reserve(b.Context(), session.Reservation{Scope: f.scope, Agent: "A", RunID: sessionRun(b)}, time.Hour)
							require.NoError(b, err)
							sessionWriteMaintenanceArchive(b, files, pending.Node, sessionBenchmarkCapture())
							due, err = f.repo.Reserve(b.Context(), session.Reservation{Scope: f.scope, Agent: "A", RunID: sessionRun(b)}, time.Hour)
							require.NoError(b, err)
							aborted, err := f.repo.AbortIntent(b.Context(), f.scope, due)
							require.NoError(b, err)
							require.True(b, aborted)
							require.NoError(b, f.repo.FinishIntent(b.Context(), f.scope, due))
							s, _ := f.repo.scopeKeys(f.scope)
							var meta sessionMeta
							found, err := sessionReadJSON(b.Context(), f.repo.client.Get(b.Context(), s.dag(due.Node.Ref.DAGID).meta), &meta)
							require.NoError(b, err)
							require.True(b, found)
							meta.LastActive -= 2 * time.Hour.Milliseconds()
							require.NoError(b, f.repo.client.Set(b.Context(), s.dag(meta.ID).meta, sessionEncode(meta), 0).Err())
							deleting, err := f.repo.ClaimDeleting(b.Context(), f.scope, time.Hour)
							require.NoError(b, err)
							require.Len(b, deleting, 1)
						}
						counter.reset()
						b.StartTimer()
						err = f.service.Recover(b.Context())
						b.StopTimer()
						require.NoError(b, err)
						stats := counter.snapshot()
						for k, n := range stats.commands {
							totals.commands[k] += n
						}
						for k, n := range stats.readValues {
							totals.readValues[k] += n
						}
						for k, n := range stats.writeValues {
							totals.writeValues[k] += n
						}
						totals.logical += stats.logical
						totals.wire += stats.wire
						totals.wireTime += stats.wireTime
						totals.pipelines += stats.pipelines
						totals.conflicts += stats.conflicts
						s, _ := f.repo.scopeKeys(f.scope)
						require.Zero(b, f.repo.client.SCard(b.Context(), s.pending).Val())
						require.Zero(b, f.repo.client.SCard(b.Context(), s.deleting).Val())
						if operation == "PendingDeleting" {
							require.EqualValues(b, roots+2, f.repo.client.SCard(b.Context(), s.dags).Val())
							publication, err := f.repo.GetPublication(b.Context(), f.scope, published.Node.RunID)
							require.NoError(b, err)
							require.NotNil(b, publication)
							require.Equal(b, published.Node.Ref, publication.Ref)
							require.Zero(b, f.repo.client.HLen(b.Context(), s.dag(published.Node.Ref.DAGID).intents).Val())
							require.Zero(b, f.repo.client.HLen(b.Context(), s.dag(pending.Node.Ref.DAGID).intents).Val())
							_, err = os.Stat(sessionArchivePath(f.dir, pending.Node))
							require.ErrorIs(b, err, os.ErrNotExist)
							require.Equal(b, sessionRedisNone, f.repo.client.Type(b.Context(), s.dag(due.Node.Ref.DAGID).meta).Val())
						}
					}
					totals.report(b)
				})
			}
		})
	}
}
