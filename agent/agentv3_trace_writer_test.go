package agentv3

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
	"time"

	"csust-got/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAgentV3TraceWriterWritesRecordsAndFlushesOnClose(t *testing.T) {
	tracePath := filepath.Join(t.TempDir(), "traces", "agentv3.jsonl")
	w := newAgentV3TraceWriter(tracePath, 0, 0, 64)
	for id := range 32 {
		require.NoError(t, w.Enqueue([]byte(`{"id":`+strconv.Itoa(id)+`}`)))
	}
	require.NoError(t, w.Close(t.Context()))
	require.Zero(t, w.Dropped())

	data, err := os.ReadFile(tracePath)
	require.NoError(t, err)
	requireCompleteAgentV3TraceJSONL(t, data, 32)
	if runtime.GOOS != "windows" {
		assert.Equal(t, fs.FileMode(0o600), mustAgentV3TraceMode(t, tracePath).Perm())
		assert.Equal(t, fs.FileMode(0o700), mustAgentV3TraceMode(t, filepath.Dir(tracePath)).Perm())
	}

	require.ErrorIs(t, w.Enqueue([]byte(`{"id":99}`)), errAgentV3TraceWriterClosed)
	require.NoError(t, w.Close(t.Context()))
}

func TestAgentV3TraceWriterDropsWhenQueueFull(t *testing.T) {
	tracePath := filepath.Join(t.TempDir(), "agentv3.jsonl")
	w := &agentV3TraceWriter{path: tracePath, queue: make(chan []byte, 2), done: make(chan struct{})}
	require.NoError(t, w.Enqueue([]byte(`{"id":1}`)))
	require.NoError(t, w.Enqueue([]byte(`{"id":2}`)))
	require.ErrorIs(t, w.Enqueue([]byte(`{"id":3}`)), errAgentV3TraceQueueFull)
	require.ErrorIs(t, w.Enqueue([]byte(`{"id":4}`)), errAgentV3TraceQueueFull)
	require.EqualValues(t, 2, w.Dropped())
	require.Len(t, w.queue, 2)
}

func TestAgentV3TraceWriterCloseHonorsContext(t *testing.T) {
	w := &agentV3TraceWriter{path: "unused.jsonl", queue: make(chan []byte, 1), done: make(chan struct{})}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	require.ErrorIs(t, w.Close(ctx), context.DeadlineExceeded)
}

func TestAgentV3TraceWriterRotatesBySize(t *testing.T) {
	tracePath := filepath.Join(t.TempDir(), "agentv3.jsonl")
	w := newAgentV3TraceWriter(tracePath, 1, 2, 1024)
	payload := []byte(`{"blob":"` + string(make([]byte, 300*1024)) + `"}`)
	for range 8 {
		require.NoError(t, w.Enqueue(payload))
	}
	require.NoError(t, w.Close(t.Context()))

	entries, err := os.ReadDir(filepath.Dir(tracePath))
	require.NoError(t, err)
	require.Greater(t, len(entries), 1, "expected rotated backups next to the trace file")
	require.LessOrEqual(t, len(entries), 3)
}

func TestEnqueueAgentV3TraceJSONLUsesWriterWhenPathMatches(t *testing.T) {
	oldConfig := config.BotConfig
	tracePath := filepath.Join(t.TempDir(), "agentv3.jsonl")
	testConfig := config.NewBotConfig()
	testConfig.AgentV3 = &config.AgentV3Config{Observability: config.AgentV3ObservabilityConfig{Enable: true, JSONLPath: tracePath, TraceQueue: 8}}
	config.BotConfig = testConfig
	t.Cleanup(func() {
		closeAgentV3TraceWriter(t.Context())
		config.BotConfig = oldConfig
	})

	initAgentV3TraceWriter()
	w := agentV3TraceWriterRef.Load()
	require.NotNil(t, w)
	require.Equal(t, tracePath, w.path)

	require.NoError(t, enqueueAgentV3TraceJSONL(t.Context(), tracePath, []byte(`{"id":0}`)))
	otherPath := filepath.Join(t.TempDir(), "other.jsonl")
	require.NoError(t, enqueueAgentV3TraceJSONL(t.Context(), otherPath, []byte(`{"id":0}`)))

	closeAgentV3TraceWriter(t.Context())
	require.Nil(t, agentV3TraceWriterRef.Load())

	data, err := os.ReadFile(tracePath)
	require.NoError(t, err)
	requireCompleteAgentV3TraceJSONL(t, data, 1)
	other, err := os.ReadFile(otherPath)
	require.NoError(t, err)
	requireCompleteAgentV3TraceJSONL(t, other, 1)

	require.NoError(t, enqueueAgentV3TraceJSONL(t.Context(), tracePath, []byte(`{"id":1}`)))
	data, err = os.ReadFile(tracePath)
	require.NoError(t, err)
	requireCompleteAgentV3TraceJSONL(t, data, 2)
}

func TestInitAgentV3TraceWriterSkipsWhenDisabled(t *testing.T) {
	oldConfig := config.BotConfig
	testConfig := config.NewBotConfig()
	testConfig.AgentV3 = &config.AgentV3Config{Observability: config.AgentV3ObservabilityConfig{Enable: false, JSONLPath: "x.jsonl"}}
	config.BotConfig = testConfig
	t.Cleanup(func() {
		closeAgentV3TraceWriter(t.Context())
		config.BotConfig = oldConfig
	})
	initAgentV3TraceWriter()
	require.Nil(t, agentV3TraceWriterRef.Load())
}
