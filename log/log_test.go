package log

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"csust-got/config"

	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"gopkg.in/natefinch/lumberjack.v2"
)

var errTestSync = errors.New("boom")

func withTestConfig(t *testing.T, mutate func(*config.Config)) {
	t.Helper()
	old := config.BotConfig
	cfg := config.NewBotConfig()
	cfg.LogFileDir = t.TempDir()
	cfg.LogConfig = &config.LogConfig{MaxSizeMB: 100, MaxBackups: 7, MaxAgeDays: 14}
	if mutate != nil {
		mutate(cfg)
	}
	config.BotConfig = cfg
	t.Cleanup(func() { config.BotConfig = old })
}

func newTestLogger(t *testing.T) *zap.Logger {
	t.Helper()
	l, closeFiles := NewLogger()
	t.Cleanup(func() { require.NoError(t, closeFiles()) })
	return l
}

func TestNewLoggerWritesRotatingFiles(t *testing.T) {
	withTestConfig(t, func(cfg *config.Config) { cfg.DebugMode = true })
	l := newTestLogger(t)
	l.Info("hello rotation", zap.String("k", "v"))
	require.NoError(t, l.Sync())

	data, err := os.ReadFile(filepath.Join(config.BotConfig.LogFileDir, logFileName))
	require.NoError(t, err)
	require.Contains(t, string(data), `"msg":"hello rotation"`)
	require.Contains(t, string(data), `"k":"v"`)
}

func TestNewLoggerProdSyncIgnoresStderr(t *testing.T) {
	withTestConfig(t, nil)
	l := newTestLogger(t)
	l.Info("prod")
	require.NoError(t, l.Sync())
}

func TestNewLoggerCloseReleasesFiles(t *testing.T) {
	withTestConfig(t, nil)
	l, closeFiles := NewLogger()
	l.Info("before close")
	l.Error("before close err")
	require.NoError(t, closeFiles())
	require.NoError(t, closeFiles())
	require.NoError(t, os.RemoveAll(config.BotConfig.LogFileDir))
}

func TestNewLoggerWithoutLogDirHasNoFiles(t *testing.T) {
	withTestConfig(t, func(cfg *config.Config) { cfg.LogFileDir = "" })
	l, closeFiles := NewLogger()
	l.Info("stderr only")
	require.NoError(t, closeFiles())
}

func TestCloseClosesInitLoggerFiles(t *testing.T) {
	withTestConfig(t, nil)
	oldLogger, oldClose, oldGlobal := logger, closeLogger, zap.L()
	t.Cleanup(func() {
		logger, closeLogger = oldLogger, oldClose
		zap.ReplaceGlobals(oldGlobal)
	})
	InitLogger()
	Info("init logger")
	closed := false
	inner := closeLogger
	closeLogger = func() error {
		closed = true
		return inner()
	}
	Close()
	require.True(t, closed)
	require.NoError(t, os.RemoveAll(config.BotConfig.LogFileDir))
}

func TestRotatingFileUsesConfig(t *testing.T) {
	compress := false
	out := rotatingFile("x.log", &config.LogConfig{MaxSizeMB: 3, MaxBackups: 2, MaxAgeDays: 1, Compress: &compress})
	require.Equal(t, &lumberjack.Logger{Filename: "x.log", MaxSize: 3, MaxBackups: 2, MaxAge: 1, LocalTime: true, Compress: false}, out)

	out = rotatingFile("y.log", nil)
	require.True(t, out.Compress)
	require.Zero(t, out.MaxSize)
}

func TestIgnoreUnsyncable(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, true},
		{"einval", &os.PathError{Op: "sync", Path: "/dev/stderr", Err: syscall.EINVAL}, true},
		{"enotty", syscall.ENOTTY, true},
		{"ebadf", syscall.EBADF, true},
		{"eio", &os.PathError{Op: "sync", Path: "/dev/stderr", Err: syscall.EIO}, false},
		{"other", errTestSync, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ignoreUnsyncable(tt.err)
			if tt.want {
				require.NoError(t, got)
				return
			}
			require.ErrorIs(t, got, tt.err)
		})
	}
}
