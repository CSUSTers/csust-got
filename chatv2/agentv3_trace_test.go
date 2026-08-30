//go:build !386 && !arm

package chatv2

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type barrierTraceWriterState struct {
	mu      sync.Mutex
	data    []byte
	entered chan struct{}
	release <-chan struct{}
}

type barrierTraceWriter struct {
	state   *barrierTraceWriterState
	entered bool
}

func (w *barrierTraceWriter) Write(data []byte) (int, error) {
	w.state.mu.Lock()
	w.state.data = append(w.state.data, data...)
	w.state.mu.Unlock()
	if !w.entered {
		w.entered = true
		w.state.entered <- struct{}{}
		<-w.state.release
	}
	return len(data), nil
}

func (s *barrierTraceWriterState) bytes() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]byte(nil), s.data...)
}

func requireCompleteAgentV3TraceJSONL(t *testing.T, data []byte, wantCount int) {
	t.Helper()
	lines := splitNonEmptyJSONLLines(data)
	require.Len(t, lines, wantCount)
	seen := make(map[int]struct{}, wantCount)
	for _, line := range lines {
		var record struct {
			ID int `json:"id"`
		}
		require.NoError(t, json.Unmarshal(line, &record))
		_, duplicate := seen[record.ID]
		require.False(t, duplicate, "duplicate trace record id %d", record.ID)
		seen[record.ID] = struct{}{}
	}
	require.Len(t, seen, wantCount)
	for id := range wantCount {
		_, ok := seen[id]
		require.True(t, ok, "missing trace record id %d", id)
	}
}

func splitNonEmptyJSONLLines(data []byte) [][]byte {
	lines := make([][]byte, 0)
	start := 0
	for i, b := range data {
		if b != '\n' {
			continue
		}
		if i > start {
			lines = append(lines, data[start:i])
		}
		start = i + 1
	}
	if start < len(data) {
		lines = append(lines, data[start:])
	}
	return lines
}

func TestAppendAgentV3TraceJSONLForcedPayloadNewlineInterleave(t *testing.T) {
	release := make(chan struct{})
	state := &barrierTraceWriterState{
		entered: make(chan struct{}, 2),
		release: release,
	}
	errs := make(chan error, 2)
	for id := range 2 {
		go func() {
			errs <- writeAgentV3TraceRecord(
				&barrierTraceWriter{state: state},
				append([]byte(`{"id":`+string(rune('0'+id))+`}`), '\n'),
			)
		}()
	}
	for range 2 {
		select {
		case <-state.entered:
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for both payload writes")
		}
	}
	close(release)
	for range 2 {
		require.NoError(t, <-errs)
	}

	requireCompleteAgentV3TraceJSONL(t, state.bytes(), 2)
}

func TestAppendAgentV3TraceJSONLConcurrentRecords(t *testing.T) {
	tracePath := filepath.Join(t.TempDir(), "traces", "agentv3.jsonl")
	errs := make(chan error, 128)
	var writers sync.WaitGroup
	for id := range 128 {
		writers.Go(func() {
			errs <- appendAgentV3TraceJSONL(tracePath, []byte(`{"id":`+strconv.Itoa(id)+`}`))
		})
	}
	writers.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	data, err := os.ReadFile(tracePath)
	require.NoError(t, err)
	requireCompleteAgentV3TraceJSONL(t, data, 128)
}

func TestAppendAgentV3TraceJSONLTightensPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix owner modes are not represented on Windows")
	}
	traceDir := filepath.Join(t.TempDir(), "traces")
	tracePath := filepath.Join(traceDir, "agentv3.jsonl")
	require.NoError(t, os.MkdirAll(traceDir, 0o755))
	require.NoError(t, os.WriteFile(tracePath, []byte("old\n"), 0o644))
	require.NoError(t, os.Chmod(traceDir, 0o755))
	require.NoError(t, os.Chmod(tracePath, 0o644))

	require.NoError(t, appendAgentV3TraceJSONL(tracePath, []byte(`{"id":1}`)))
	assert.Equal(t, fs.FileMode(0o700), mustAgentV3TraceMode(t, traceDir).Perm())
	assert.Equal(t, fs.FileMode(0o600), mustAgentV3TraceMode(t, tracePath).Perm())
}

func mustAgentV3TraceMode(t *testing.T, path string) fs.FileMode {
	t.Helper()
	info, err := os.Stat(path)
	require.NoError(t, err)
	return info.Mode()
}
