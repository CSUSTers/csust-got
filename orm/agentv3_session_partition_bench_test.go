package orm

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"testing"
	"time"

	"csust-got/agent/session"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func sessionStoreFixture(t testing.TB, f *sessionFixture, state sessionState) {
	t.Helper()
	s, err := f.repo.scopeKeys(f.scope)
	require.NoError(t, err)
	_, err = f.repo.client.Pipelined(t.Context(), func(p redis.Pipeliner) error {
		if len(state.DAGs) == 0 {
			return nil
		}
		p.Set(t.Context(), s.sequence, strconv.FormatInt(state.Sequence, 10), 0)
		p.HSet(t.Context(), f.repo.base+"scopes", f.scope.Key(), sessionEncode(f.scope))
		for id, view := range state.DAGs {
			d := s.dag(id)
			p.SAdd(t.Context(), s.dags, id)
			p.Set(t.Context(), d.meta, sessionEncode(sessionMeta{Layout: sessionRedisLayout, Scope: f.scope, ID: view.ID, Generation: view.Generation, State: view.State, LastActive: view.LastActive}), 0)
			for field, n := range view.Nodes {
				p.HSet(t.Context(), d.nodes, field, sessionEncode(n))
				if view.State == sessionDAGActive {
					p.ZAdd(t.Context(), s.latest(n.Agent), redis.Z{Score: 0, Member: sessionLatestMember(n)})
				}
			}
			for field, i := range view.Intents {
				p.HSet(t.Context(), d.intents, field, sessionEncode(i))
				p.HSet(t.Context(), s.runs, field, sessionEncode(sessionRunIndex{Ref: i.Node.Ref, Status: i.Status}))
			}
			for field, l := range view.Leases {
				p.HSet(t.Context(), d.leases, field, sessionEncode(l))
			}
		}
		for id, ref := range state.Runs {
			p.HSet(t.Context(), s.runs, id, sessionEncode(sessionRunIndex{Ref: ref, Status: sessionIntentPublished}))
		}
		for id, ref := range state.Messages {
			p.HSet(t.Context(), s.messages, id, sessionEncode(ref))
		}
		return nil
	})
	require.NoError(t, err)
}

func sessionPartitionFixture(t testing.TB, f *sessionFixture, shape string, count int) sessionState {
	t.Helper()
	state := sessionBenchmarkState(f.scope, f.now.UnixMilli(), shape, count)
	files, err := session.NewFileStore(f.dir)
	require.NoError(t, err)
	defer files.Close()
	rootCapture := sessionBenchmarkCapture()
	require.NoError(t, files.WithScopeLock(t.Context(), f.scope, func(fs *session.ScopeFiles) error {
		for _, dag := range state.DAGs {
			for id, node := range dag.Nodes {
				capture := rootCapture
				if node.Parent != nil {
					capture.Bootstrap = nil
				}
				digest, size, err := fs.WriteAtomic(node, capture)
				if err != nil {
					return err
				}
				node.Digest, node.Size = digest, size
				dag.Nodes[id] = node
			}
		}
		return nil
	}))
	sessionStoreFixture(t, f, state)
	return state
}

func sessionRestoreFixture(t testing.TB, f *sessionFixture, state sessionState) {
	t.Helper()
	keys := f.mr.Keys()
	if len(keys) > 0 {
		require.NoError(t, f.repo.client.Del(t.Context(), keys...).Err())
	}
	sessionStoreFixture(t, f, state)
}

func sessionAdvanceFixture(t testing.TB, f *sessionFixture) {
	t.Helper()
	if clock, ok := f.mr.(interface{ advanceClock() time.Time }); ok {
		f.now = clock.advanceClock()
	} else {
		f.now = f.now.Add(time.Millisecond)
		f.mr.SetTime(f.now)
	}
}

func BenchmarkAgentV3SessionPartition(b *testing.B) {
	for _, shape := range []string{"roots", "chain", "fork"} {
		for _, count := range []int{1, 100, 1000} {
			b.Run(fmt.Sprintf("%s%d", shape, count), func(b *testing.B) {
				f := newSessionFixture(b, session.Options{TTL: 7 * 24 * time.Hour, LeaseDuration: time.Hour, RenewInterval: 30 * time.Minute})
				state := sessionPartitionFixture(b, f, shape, count)
				files, err := session.NewFileStore(f.dir)
				require.NoError(b, err)
				b.Cleanup(func() { require.NoError(b, files.Close()) })
				counter := sessionCountCommands(b, f)
				operations := []string{"Renew", "ConfirmLoaded", "ReserveChild", "ReserveRoot", "Publish", "DefaultRoot", "LoadBranch", "CollectIdle"}
				if shape != "roots" {
					operations = []string{"Renew", "ReserveChild", "Publish", "Load"}
				}
				for _, operation := range operations {
					b.Run(operation, func(b *testing.B) {
						var totals sessionCommandStats
						totals.commands, totals.readValues, totals.writeValues = map[string]int64{}, map[string]int64{}, map[string]int64{}
						var disk int64
						b.ReportAllocs()
						b.ResetTimer()
						for range b.N {
							b.StopTimer()
							sessionRestoreFixture(b, f, state)
							sessionAdvanceFixture(b, f)
							messageID := 101
							if shape != "roots" {
								messageID = count + 100
							}
							sel := session.Selection{Scope: f.scope, Mode: session.SelectReply, ReplyMessageID: messageID}
							var pinned session.Pinned
							var intent session.Intent
							var digest string
							var size int64
							var err error
							if operation == "Renew" || operation == "ConfirmLoaded" || operation == "ReserveChild" || operation == "Publish" {
								pinned, err = f.repo.ResolveAndPin(b.Context(), sel, sessionRun(b), time.Hour)
								require.NoError(b, err)
							}
							if operation == "Publish" {
								ref := pinned.Nodes[len(pinned.Nodes)-1].Ref
								intent, err = f.repo.Reserve(b.Context(), session.Reservation{Scope: f.scope, Agent: "A", RunID: sessionRun(b), Parent: &ref, Lease: &pinned.Lease}, time.Hour)
								require.NoError(b, err)
								require.NoError(b, files.WithScopeLock(b.Context(), f.scope, func(fs *session.ScopeFiles) error {
									capture := sessionBenchmarkCapture()
									capture.Bootstrap = nil
									var err error
									digest, size, err = fs.WriteAtomic(intent.Node, capture)
									return err
								}))
							}
							sessionAdvanceFixture(b, f)
							counter.reset()
							req := session.CommitRequest{Scope: f.scope, Agent: "A", RunID: sessionRun(b), Capture: sessionBenchmarkCapture(), Receipt: session.DeliveryReceipt{MessageIDs: []int{10001}}}
							var created session.Node
							b.StartTimer()
							switch operation {
							case "Renew":
								err = f.repo.Renew(b.Context(), f.scope, pinned.Lease, time.Hour)
							case "ConfirmLoaded":
								err = f.repo.ConfirmLoaded(b.Context(), f.scope, pinned.Lease, time.Hour)
							case "ReserveChild":
								ref := pinned.Nodes[len(pinned.Nodes)-1].Ref
								intent, err = f.repo.Reserve(b.Context(), session.Reservation{Scope: f.scope, Agent: "A", RunID: req.RunID, Parent: &ref, Lease: &pinned.Lease}, time.Hour)
							case "ReserveRoot":
								intent, err = f.repo.Reserve(b.Context(), session.Reservation{Scope: f.scope, Agent: "A", RunID: req.RunID}, time.Hour)
							case "Publish":
								created, err = f.repo.Publish(b.Context(), f.scope, intent, digest, size, req.Receipt)
							case "DefaultRoot":
								created, err = f.service.Commit(b.Context(), req)
							case "Load", "LoadBranch":
								var loaded session.LoadResult
								loaded, err = f.service.Load(b.Context(), sel)
								if err == nil {
									want := 5
									if shape == "chain" {
										want += 2 * (count - 1)
									}
									if shape != "fork" && len(loaded.Messages) != want {
										b.Fatalf("incomplete replay: got %d want %d", len(loaded.Messages), want)
									}
									if operation == "LoadBranch" {
										b.StopTimer()
										sessionAdvanceFixture(b, f)
										b.StartTimer()
										req.Parent, req.Capture.Bootstrap = loaded.Parent, nil
										created, err = f.service.Commit(b.Context(), req)
									}
									closeErr := loaded.Parent.Close()
									if err == nil {
										err = closeErr
									}
								}
							case "CollectIdle":
								err = f.service.Collect(b.Context())
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
							totals.pipelines += s.pipelines
							totals.conflicts += s.conflicts
							if operation == "Renew" || operation == "ConfirmLoaded" {
								current := sessionReadState(b, f).DAGs[pinned.Lease.DAGID].Leases[pinned.Lease.Token]
								require.Greater(b, current.Deadline, pinned.Lease.Deadline)
							}
							if operation == "Publish" || operation == "DefaultRoot" || operation == "LoadBranch" {
								require.Equal(b, int64(count+1), created.CommitSequence)
							}
							if operation == "DefaultRoot" || operation == "LoadBranch" || operation == "Publish" {
								disk += created.Size
								require.NoError(b, os.Remove(sessionArchivePath(f.dir, created)))
								if created.Parent == nil {
									require.NoError(b, files.WithScopeLock(b.Context(), f.scope, func(fs *session.ScopeFiles) error { return fs.RemoveEmptyDAG(created.Ref.DAGID) }))
								}
							}
							if operation == "ReserveRoot" || operation == "ReserveChild" {
								require.Equal(b, req.RunID, intent.Node.RunID)
							}
						}
						b.ReportMetric(float64(disk)/float64(b.N), "JSONL-B/op")
						b.ReportMetric(float64(count), "fixture-nodes")
						totals.report(b)
					})
				}
			})
		}
	}
}

func TestAgentV3SessionRootRejectsMisboundCatalog(t *testing.T) {
	f := newSessionFixture(t, session.Options{})
	other := f.scope
	other.ChatID++
	data, err := json.Marshal(other)
	require.NoError(t, err)
	require.NoError(t, f.repo.client.HSet(t.Context(), f.repo.base+"scopes", f.scope.Key(), data).Err())
	_, err = f.repo.Reserve(t.Context(), session.Reservation{Scope: f.scope, Agent: "A", RunID: sessionRun(t)}, time.Hour)
	require.ErrorIs(t, err, session.ErrCorrupt)
	require.Equal(t, string(data), f.repo.client.HGet(t.Context(), f.repo.base+"scopes", f.scope.Key()).Val())
}
