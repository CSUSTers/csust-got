package orm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"csust-got/agent/session"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func sessionAssertNoop(t *testing.T, f *sessionFixture, counter *sessionCommandCounter, operation func()) {
	t.Helper()
	before := sessionReadState(t, f)
	catalog := f.repo.client.HGetAll(t.Context(), f.repo.base+"scopes").Val()
	counter.reset()
	operation()
	stats := counter.snapshot()
	for _, name := range []string{"set", "hset", "del", "hdel", "sadd", "srem", "zadd", "zrem"} {
		require.Zero(t, stats.commands[name], name)
	}
	require.Greater(t, stats.commands["watch"], int64(0))
	require.Zero(t, stats.setBytes)
	require.Equal(t, before, sessionReadState(t, f))
	require.Equal(t, catalog, f.repo.client.HGetAll(t.Context(), f.repo.base+"scopes").Val())
}

func TestAgentV3SessionIdempotentMutationsDoNotWrite(t *testing.T) {
	f := newSessionFixture(t, session.Options{})
	counter := sessionCountCommands(t, f)
	missing := session.Intent{Node: session.Node{Ref: session.NodeRef{DAGID: sessionRun(t), NodeID: sessionRun(t)}, RunID: sessionRun(t)}}
	sessionAssertNoop(t, f, counter, func() { require.NoError(t, f.repo.FinishIntent(t.Context(), f.scope, missing)) })
	sessionAssertNoop(t, f, counter, func() {
		require.NoError(t, f.repo.FinishDelete(t.Context(), f.scope, session.Deletion{DAGID: missing.Node.Ref.DAGID}))
	})
	sessionAssertNoop(t, f, counter, func() {
		require.NoError(t, f.repo.Release(t.Context(), f.scope, session.Lease{DAGID: missing.Node.Ref.DAGID}))
	})
	req := session.Reservation{Scope: f.scope, Agent: "A", RunID: sessionRun(t)}
	intent, err := f.repo.Reserve(t.Context(), req, time.Minute)
	require.NoError(t, err)
	sessionAssertNoop(t, f, counter, func() {
		repeated, err := f.repo.Reserve(t.Context(), req, time.Minute)
		require.NoError(t, err)
		require.Equal(t, intent, repeated)
	})
	digest := strings.Repeat("a", 64)
	receipt := session.DeliveryReceipt{MessageIDs: []int{101, 102}}
	node, err := f.repo.Publish(t.Context(), f.scope, intent, digest, 10, receipt)
	require.NoError(t, err)
	sessionAssertNoop(t, f, counter, func() {
		repeated, err := f.repo.Publish(t.Context(), f.scope, intent, digest, 10, receipt)
		require.NoError(t, err)
		require.Equal(t, node, repeated)
	})
	sessionAssertNoop(t, f, counter, func() {
		aborted, err := f.repo.AbortIntent(t.Context(), f.scope, intent)
		require.NoError(t, err)
		require.False(t, aborted)
	})
	require.NoError(t, f.repo.FinishIntent(t.Context(), f.scope, intent))
	sessionAssertNoop(t, f, counter, func() { require.NoError(t, f.repo.FinishIntent(t.Context(), f.scope, intent)) })
	sessionAssertNoop(t, f, counter, func() { require.NoError(t, f.repo.Release(t.Context(), f.scope, intent.Lease)) })
	sessionAssertNoop(t, f, counter, func() {
		claimed, err := f.repo.ClaimDeleting(t.Context(), f.scope, time.Hour)
		require.NoError(t, err)
		require.Empty(t, claimed)
	})
	abortReq := session.Reservation{Scope: f.scope, Agent: "A", RunID: sessionRun(t)}
	abortIntent, err := f.repo.Reserve(t.Context(), abortReq, time.Minute)
	require.NoError(t, err)
	aborted, err := f.repo.AbortIntent(t.Context(), f.scope, abortIntent)
	require.NoError(t, err)
	require.True(t, aborted)
	sessionAssertNoop(t, f, counter, func() {
		aborted, err := f.repo.AbortIntent(t.Context(), f.scope, abortIntent)
		require.NoError(t, err)
		require.True(t, aborted)
	})
	require.NoError(t, f.repo.FinishIntent(t.Context(), f.scope, abortIntent))
	f.mr.SetTime(f.now.Add(2 * time.Hour))
	claimed, err := f.repo.ClaimDeleting(t.Context(), f.scope, time.Hour)
	require.NoError(t, err)
	require.Len(t, claimed, 2)
	sessionAssertNoop(t, f, counter, func() {
		repeated, err := f.repo.ClaimDeleting(t.Context(), f.scope, time.Hour)
		require.NoError(t, err)
		require.ElementsMatch(t, claimed, repeated)
	})
	for _, deletion := range claimed {
		require.NoError(t, f.repo.FinishDelete(t.Context(), f.scope, deletion))
	}
	sessionAssertNoop(t, f, counter, func() { require.NoError(t, f.repo.FinishDelete(t.Context(), f.scope, claimed[0])) })
}

func TestAgentV3SessionPublishedMismatchNeverReturnsNode(t *testing.T) {
	f := newSessionFixture(t, session.Options{})
	intent, err := f.repo.Reserve(t.Context(), session.Reservation{Scope: f.scope, Agent: "A", RunID: sessionRun(t)}, time.Minute)
	require.NoError(t, err)
	digest := strings.Repeat("a", 64)
	receipt := session.DeliveryReceipt{MessageIDs: []int{101, 102}}
	_, err = f.repo.Publish(t.Context(), f.scope, intent, digest, 10, receipt)
	require.NoError(t, err)
	counter := sessionCountCommands(t, f)
	for _, mismatch := range []string{"receipt", "agent", "parent", "digest", "size"} {
		t.Run(mismatch, func(t *testing.T) {
			wrongIntent, wrongDigest, wrongSize, wrongReceipt := intent, digest, int64(10), receipt
			switch mismatch {
			case "receipt":
				wrongReceipt.MessageIDs = []int{102, 101}
			case "agent":
				wrongIntent.Node.Agent = "B"
			case "parent":
				wrongIntent.Node.Parent = &session.NodeRef{DAGID: sessionRun(t), NodeID: sessionRun(t)}
			case "digest":
				wrongDigest = strings.Repeat("b", 64)
			case "size":
				wrongSize++
			}
			sessionAssertNoop(t, f, counter, func() {
				node, err := f.repo.Publish(t.Context(), f.scope, wrongIntent, wrongDigest, wrongSize, wrongReceipt)
				require.ErrorIs(t, err, session.ErrCorrupt)
				require.Equal(t, session.Node{}, node)
			})
		})
	}
}

func TestAgentV3SessionLeaseChangesAndPruningStillWrite(t *testing.T) {
	f := newSessionFixture(t, session.Options{TTL: time.Hour})
	root, err := f.service.Commit(t.Context(), sessionRequest(t, f.scope, "A", nil, 101, "root"))
	require.NoError(t, err)
	sel := session.Selection{Scope: f.scope, Mode: session.SelectReply, ReplyMessageID: 101}
	expired, err := f.repo.ResolveAndPin(t.Context(), sel, sessionRun(t), time.Minute)
	require.NoError(t, err)
	live, err := f.repo.ResolveAndPin(t.Context(), sel, sessionRun(t), 3*time.Hour)
	require.NoError(t, err)
	counter := sessionCountCommands(t, f)
	f.mr.SetTime(f.now.Add(123 * time.Millisecond))
	require.NoError(t, f.repo.Renew(t.Context(), f.scope, live.Lease, 3*time.Hour))
	require.EqualValues(t, 1, counter.snapshot().commands["hset"])
	state := sessionReadState(t, f)
	require.Equal(t, f.now.UnixMilli(), state.DAGs[root.Ref.DAGID].LastActive)
	require.Equal(t, f.now.Add(3*time.Hour+123*time.Millisecond).UnixMilli(), state.DAGs[root.Ref.DAGID].Leases[live.Lease.Token].Deadline)
	sessionAssertNoop(t, f, counter, func() { require.NoError(t, f.repo.Renew(t.Context(), f.scope, live.Lease, 3*time.Hour)) })
	counter.reset()
	require.NoError(t, f.repo.ConfirmLoaded(t.Context(), f.scope, live.Lease, 3*time.Hour))
	require.EqualValues(t, 1, counter.snapshot().commands["exec"])
	require.Equal(t, f.now.Add(123*time.Millisecond).UnixMilli(), sessionReadState(t, f).DAGs[root.Ref.DAGID].LastActive)
	f.mr.SetTime(f.now.Add(2 * time.Hour))
	counter.reset()
	claimed, err := f.repo.ClaimDeleting(t.Context(), f.scope, time.Hour)
	require.NoError(t, err)
	require.Empty(t, claimed, "live pin prevents deletion but not expired lease pruning")
	require.EqualValues(t, 1, counter.snapshot().commands["hdel"])
	state = sessionReadState(t, f)
	require.NotContains(t, state.DAGs[root.Ref.DAGID].Leases, expired.Lease.Token)
	require.Contains(t, state.DAGs[root.Ref.DAGID].Leases, live.Lease.Token)
	sessionAssertNoop(t, f, counter, func() {
		claimed, err := f.repo.ClaimDeleting(t.Context(), f.scope, time.Hour)
		require.NoError(t, err)
		require.Empty(t, claimed)
	})
	require.ErrorIs(t, f.repo.Renew(t.Context(), f.scope, expired.Lease, time.Hour), session.ErrFence)
}

func TestAgentV3SessionIdleCollectDoesNotRewriteState(t *testing.T) {
	f := newSessionFixture(t, session.Options{TTL: 7 * 24 * time.Hour})
	sessionSeedBenchmark(t, f, 100)
	baseline := sessionReadState(t, f)
	counter := sessionCountCommands(t, f)
	f.mr.SetTime(f.now.Add(24 * time.Hour))
	require.NoError(t, f.service.Collect(t.Context()))
	stats := counter.snapshot()
	for _, name := range []string{"set", "hset", "del", "hdel", "sadd", "srem", "zadd", "zrem"} {
		require.Zero(t, stats.commands[name], name)
	}
	require.Equal(t, baseline, sessionReadState(t, f))
	for _, dag := range sessionReadState(t, f).DAGs {
		require.Equal(t, f.now.UnixMilli(), dag.LastActive)
	}
}

func TestAgentV3SessionActiveLoadedBranchStillPerformsRealWrites(t *testing.T) {
	f := newSessionFixture(t, session.Options{})
	sessionSeedBenchmark(t, f, 1)
	counter := sessionCountCommands(t, f)
	f.mr.SetTime(f.now.Add(time.Millisecond))
	loaded, err := f.service.Load(t.Context(), session.Selection{Scope: f.scope, Mode: session.SelectReply, ReplyMessageID: 101})
	require.NoError(t, err)
	f.mr.SetTime(f.now.Add(2 * time.Millisecond))
	_, err = f.service.Commit(t.Context(), sessionRequest(t, f.scope, "B", loaded.Parent, 102, "branch"))
	require.NoError(t, err)
	require.NoError(t, loaded.Parent.Close())
	stats := counter.snapshot()
	require.EqualValues(t, 1, stats.commands["hgetall"], "only the selected DAG nodes are read in full")
	require.EqualValues(t, 8, stats.commands["exec"])
	state := sessionReadState(t, f)
	require.Equal(t, f.now.Add(2*time.Millisecond).UnixMilli(), state.DAGs[loaded.Parent.Ref().DAGID].LastActive)
}

type sessionCatalogErrorHook struct{}

func (*sessionCatalogErrorHook) DialHook(next redis.DialHook) redis.DialHook { return next }
func (*sessionCatalogErrorHook) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return next
}
func (*sessionCatalogErrorHook) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error {
		if cmd.Name() == "hgetall" {
			return errSessionDisconnected
		}
		return next(ctx, cmd)
	}
}

func TestAgentV3SessionScopesReturnsHealthyEntriesAndSafeErrors(t *testing.T) {
	f := newSessionFixture(t, session.Options{})
	_, err := f.service.Commit(t.Context(), sessionRequest(t, f.scope, "A", nil, 101, "root"))
	require.NoError(t, err)
	other := f.scope
	other.ChatID++
	encoded, err := json.Marshal(other)
	require.NoError(t, err)
	wrongNamespace := f.scope
	wrongNamespace.Namespace = strings.Repeat("f", 64)
	foreign, err := json.Marshal(wrongNamespace)
	require.NoError(t, err)
	badEntries := map[string]string{"bad\nfield": `{"secret":"raw catalog payload"`, "invalid": `{"namespace":"private invalid value"}`, "foreign": string(foreign), "mismatched": string(encoded)}
	catalog := f.repo.base + "scopes"
	for key, value := range badEntries {
		require.NoError(t, f.repo.client.HSet(t.Context(), catalog, key, value).Err())
	}
	require.NoError(t, f.repo.client.HSet(t.Context(), catalog, other.Key(), encoded).Err())
	before := f.repo.client.HGetAll(t.Context(), catalog).Val()
	counter := sessionCountCommands(t, f)
	scopes, err := f.repo.Scopes(t.Context())
	require.ErrorIs(t, err, session.ErrCorrupt)
	require.ElementsMatch(t, []session.Scope{f.scope, other}, scopes)
	for key := range badEntries {
		require.Contains(t, err.Error(), fmt.Sprintf("%q", key))
	}
	require.NotContains(t, err.Error(), "raw catalog payload")
	require.NotContains(t, err.Error(), "private invalid value")
	require.NotContains(t, err.Error(), "bad\nfield")
	require.Zero(t, counter.snapshot().commands["hdel"])
	require.Equal(t, before, f.repo.client.HGetAll(t.Context(), catalog).Val())
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	scopes, err = f.repo.Scopes(ctx)
	require.ErrorIs(t, err, context.Canceled)
	require.Nil(t, scopes)
	f.repo.client.AddHook(&sessionCatalogErrorHook{})
	scopes, err = f.repo.Scopes(t.Context())
	require.ErrorIs(t, err, errSessionDisconnected)
	require.Nil(t, scopes, "a failed Redis catalog read has no trusted partial results")
}

func TestAgentV3SessionBadCatalogDoesNotBlockHealthyMaintenance(t *testing.T) {
	f := newSessionFixture(t, session.Options{TTL: time.Hour})
	root, err := f.service.Commit(t.Context(), sessionRequest(t, f.scope, "A", nil, 101, "root"))
	require.NoError(t, err)
	bad := `{"private":"bad catalog value"`
	catalog := f.repo.base + "scopes"
	require.NoError(t, f.repo.client.HSet(t.Context(), catalog, "unknown", bad).Err())
	f.mr.SetTime(f.now.Add(2 * time.Hour))
	require.ErrorIs(t, f.service.Collect(t.Context()), session.ErrCorrupt)
	require.Empty(t, sessionReadState(t, f).DAGs)
	_, err = os.Stat(sessionArchivePath(f.dir, root))
	require.ErrorIs(t, err, os.ErrNotExist)
	require.Equal(t, bad, f.repo.client.HGet(t.Context(), catalog, "unknown").Val())
	require.Equal(t, map[string]string{"unknown": bad}, f.repo.client.HGetAll(t.Context(), catalog).Val())
}

type sessionBeforeExecHook struct {
	once  sync.Once
	run   func(context.Context) error
	match func([]redis.Cmder) bool
}

func (h *sessionBeforeExecHook) DialHook(next redis.DialHook) redis.DialHook          { return next }
func (h *sessionBeforeExecHook) ProcessHook(next redis.ProcessHook) redis.ProcessHook { return next }
func (h *sessionBeforeExecHook) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return func(ctx context.Context, cmds []redis.Cmder) error {
		var err error
		if len(cmds) > 0 && cmds[0].Name() == "multi" && (h.match == nil || h.match(cmds)) {
			h.once.Do(func() { err = h.run(ctx) })
		}
		if err != nil {
			return err
		}
		return next(ctx, cmds)
	}
}

func TestAgentV3SessionWatchRetryResetsReservationOutcome(t *testing.T) {
	f := newSessionFixture(t, session.Options{})
	counter := sessionCountCommands(t, f)
	client := redis.NewClient(&redis.Options{Addr: f.mr.Addr(), MaxRetries: -1})
	f.mr.configureClient(client)
	t.Cleanup(func() { _ = client.Close() })
	other, err := NewAgentV3SessionRepository(client, "session-test:")
	require.NoError(t, err)
	req := session.Reservation{Scope: f.scope, Agent: "A", RunID: sessionRun(t)}
	var competing session.Intent
	f.repo.client.AddHook(&sessionBeforeExecHook{run: func(ctx context.Context) error {
		var err error
		competing, err = other.Reserve(ctx, req, time.Minute)
		return err
	}})
	intent, err := f.repo.Reserve(t.Context(), req, time.Minute)
	require.NoError(t, err)
	require.Equal(t, competing, intent)
	stats := counter.snapshot()
	require.EqualValues(t, 5, stats.commands["watch"])
	require.EqualValues(t, 2, stats.commands["exec"], "retry validates the competing reservation snapshot")
	require.EqualValues(t, 1, stats.conflicts)
	require.Len(t, sessionReadState(t, f).DAGs, 1)
}

func TestAgentV3SessionWatchRetryResetsDeletionManifest(t *testing.T) {
	f := newSessionFixture(t, session.Options{TTL: time.Hour})
	root, err := f.service.Commit(t.Context(), sessionRequest(t, f.scope, "A", nil, 101, "root"))
	require.NoError(t, err)
	f.mr.SetTime(f.now.Add(2 * time.Hour))
	client := redis.NewClient(&redis.Options{Addr: f.mr.Addr(), MaxRetries: -1})
	f.mr.configureClient(client)
	t.Cleanup(func() { _ = client.Close() })
	other, err := NewAgentV3SessionRepository(client, "session-test:")
	require.NoError(t, err)
	counter := sessionCountCommands(t, f)
	s, _ := f.repo.scopeKeys(f.scope)
	f.repo.client.AddHook(&sessionBeforeExecHook{match: func(cmds []redis.Cmder) bool {
		for _, cmd := range cmds {
			if cmd.Name() == "set" && fmt.Sprint(cmd.Args()[1]) == s.dag(root.Ref.DAGID).meta {
				return true
			}
		}
		return false
	}, run: func(ctx context.Context) error {
		_, err := other.ResolveAndPin(ctx, session.Selection{Scope: f.scope, Mode: session.SelectReply, ReplyMessageID: 101}, sessionRun(t), time.Hour)
		return err
	}})
	claimed, err := f.repo.ClaimDeleting(t.Context(), f.scope, time.Hour)
	require.NoError(t, err)
	require.Empty(t, claimed, "failed attempt's deleting manifest must not escape after the live-pin retry")
	require.EqualValues(t, 1, counter.snapshot().conflicts)
	state := sessionReadState(t, f)
	require.Equal(t, sessionDAGActive, state.DAGs[root.Ref.DAGID].State)
	require.Len(t, state.DAGs[root.Ref.DAGID].Leases, 1)
}

func TestAgentV3SessionWatchRetryDoesNotCompensatePublishedIntent(t *testing.T) {
	f := newSessionFixture(t, session.Options{})
	intent, err := f.repo.Reserve(t.Context(), session.Reservation{Scope: f.scope, Agent: "A", RunID: sessionRun(t)}, time.Minute)
	require.NoError(t, err)
	client := redis.NewClient(&redis.Options{Addr: f.mr.Addr(), MaxRetries: -1})
	f.mr.configureClient(client)
	t.Cleanup(func() { _ = client.Close() })
	other, err := NewAgentV3SessionRepository(client, "session-test:")
	require.NoError(t, err)
	counter := sessionCountCommands(t, f)
	f.repo.client.AddHook(&sessionBeforeExecHook{run: func(ctx context.Context) error {
		_, err := other.Publish(ctx, f.scope, intent, strings.Repeat("a", 64), 10, session.DeliveryReceipt{MessageIDs: []int{101}})
		return err
	}})
	aborted, err := f.repo.AbortIntent(t.Context(), f.scope, intent)
	require.NoError(t, err)
	require.False(t, aborted, "the failed abort outcome must not authorize file compensation")
	require.EqualValues(t, 1, counter.snapshot().conflicts)
	published, err := f.repo.GetPublication(t.Context(), f.scope, intent.Node.RunID)
	require.NoError(t, err)
	require.NotNil(t, published)
}

func TestAgentV3SessionPartitionArchiveRoundTrip(t *testing.T) {
	f := newSessionFixture(t, session.Options{})
	sessionSeedBenchmark(t, f, 1)
	old := sessionReadState(t, f)
	root := old.DAGs[fmt.Sprintf("%032x", 1)].Nodes[fmt.Sprintf("%032x", 20000)]
	archiveBefore, err := os.ReadFile(sessionArchivePath(f.dir, root))
	require.NoError(t, err)
	loaded := fixtureLoad(t, f, f.service, 101)
	require.Len(t, loaded.Messages, 5)
	require.Equal(t, strings.Repeat("A", 16384), *loaded.Messages[1].UserInputMultiContent[0].Image.Base64Data)
	child, err := f.service.Commit(t.Context(), sessionRequest(t, f.scope, "B", loaded.Parent, 102, "v1 continuation"))
	require.NoError(t, err)
	require.NoError(t, loaded.Parent.Close())
	require.Equal(t, root.Ref, *child.Parent)
	archiveAfter, err := os.ReadFile(sessionArchivePath(f.dir, root))
	require.NoError(t, err)
	require.Equal(t, archiveBefore, archiveAfter)

	state := sessionReadState(t, f)
	require.Equal(t, child.Ref, state.Messages["102"])
	require.Equal(t, child.Ref, state.Runs[child.RunID])
	require.Equal(t, old.DAGs[root.Ref.DAGID].LastActive, state.DAGs[root.Ref.DAGID].LastActive)
	files, err := session.NewFileStore(f.dir)
	require.NoError(t, err)
	defer files.Close()
	require.NoError(t, files.WithScopeLock(t.Context(), f.scope, func(fs *session.ScopeFiles) error {
		capture, err := fs.Read(child)
		if err != nil {
			return err
		}
		require.Empty(t, capture.Bootstrap)
		require.Equal(t, "v1 continuation", capture.Delta[0].Message.Content)
		return nil
	}))

	// Redis layout changes do not change the single JSONL envelope or SDK records.
	var header struct {
		Kind           string           `json:"kind"`
		Version        int              `json:"version"`
		Scope          session.Scope    `json:"scope"`
		Ref            session.NodeRef  `json:"ref"`
		Parent         *session.NodeRef `json:"parent,omitempty"`
		Agent          string           `json:"agent"`
		RunID          string           `json:"run_id"`
		Complete       bool             `json:"complete"`
		FrameCount     int              `json:"frame_count"`
		BootstrapCount int              `json:"bootstrap_count"`
		DeltaCount     int              `json:"delta_count"`
	}
	newArchive, err := os.ReadFile(sessionArchivePath(f.dir, child))
	require.NoError(t, err)
	decoder := json.NewDecoder(bytes.NewReader(newArchive))
	decoder.DisallowUnknownFields()
	require.NoError(t, decoder.Decode(&header))
	require.Equal(t, 1, header.Version)
	require.Equal(t, root.Ref, *header.Parent)
	require.True(t, header.Complete)
	for range header.FrameCount + header.BootstrapCount + header.DeltaCount {
		var record session.Record
		require.NoError(t, decoder.Decode(&record))
		require.NotNil(t, record.Message)
	}
	var trailer struct {
		Kind  string `json:"kind"`
		Count int    `json:"count"`
	}
	require.NoError(t, decoder.Decode(&trailer))
	require.Equal(t, "complete", trailer.Kind)
	require.Equal(t, header.FrameCount+header.BootstrapCount+header.DeltaCount, trailer.Count)
	require.ErrorIs(t, decoder.Decode(&trailer), io.EOF)
}
