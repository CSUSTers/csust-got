package orm

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"csust-got/agent/session"

	"github.com/stretchr/testify/require"
)

func BenchmarkAgentV3SessionConcurrent(b *testing.B) {
	for _, mode := range []string{"SameDAGRenew", "DifferentDAGRenew", "ScopePublish"} {
		b.Run(mode, func(b *testing.B) {
			f := newSessionFixture(b, session.Options{})
			sessionPartitionFixture(b, f, "roots", 100)
			pins := make([]session.Pinned, 2)
			for i := range pins {
				id := 101
				if mode == "DifferentDAGRenew" {
					id += i
				}
				var err error
				pins[i], err = f.repo.ResolveAndPin(b.Context(), session.Selection{Scope: f.scope, Mode: session.SelectReply, ReplyMessageID: id}, sessionRun(b), time.Hour)
				require.NoError(b, err)
			}
			intents := make([]session.Intent, b.N)
			digests := make([]string, b.N)
			sizes := make([]int64, b.N)
			if mode == "ScopePublish" {
				files, err := session.NewFileStore(f.dir)
				require.NoError(b, err)
				b.Cleanup(func() { require.NoError(b, files.Close()) })
				ref := pins[0].Nodes[0].Ref
				for i := range intents {
					var err error
					intents[i], err = f.repo.Reserve(b.Context(), session.Reservation{Scope: f.scope, Agent: "A", RunID: sessionRun(b), Parent: &ref, Lease: &pins[0].Lease}, time.Hour)
					require.NoError(b, err)
					require.NoError(b, files.WithScopeLock(b.Context(), f.scope, func(fs *session.ScopeFiles) error {
						capture := sessionBenchmarkCapture()
						capture.Bootstrap = nil
						var err error
						digests[i], sizes[i], err = fs.WriteAtomic(intents[i].Node, capture)
						return err
					}))
				}
			}
			counter := sessionCountCommands(b, f)
			var workers, sequence, success atomic.Int64
			var clockMu sync.Mutex
			now := f.now
			b.ReportAllocs()
			b.ResetTimer()
			b.RunParallel(func(pb *testing.PB) {
				worker := int(workers.Add(1)-1) % len(pins)
				for pb.Next() {
					clockMu.Lock()
					now = now.Add(time.Millisecond)
					f.mr.SetTime(now)
					clockMu.Unlock()
					var err error
					if mode == "ScopePublish" {
						i := int(sequence.Add(1) - 1)
						_, err = f.repo.Publish(b.Context(), f.scope, intents[i], digests[i], sizes[i], session.DeliveryReceipt{MessageIDs: []int{10001 + i}})
					} else {
						err = f.repo.Renew(b.Context(), f.scope, pins[worker].Lease, time.Hour)
					}
					if err != nil {
						b.Error(err)
					} else {
						success.Add(1)
					}
				}
			})
			b.StopTimer()
			counter.snapshot().report(b)
			require.EqualValues(b, b.N, success.Load())
			if mode == "ScopePublish" {
				require.Equal(b, int64(100+b.N), sessionReadState(b, f).Sequence)
			}
		})
	}
}
