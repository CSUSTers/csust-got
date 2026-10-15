package log

import (
	"csust-got/config"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/samber/lo"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"gopkg.in/natefinch/lumberjack.v2"
)

const (
	logFileName    = "got.log"
	errLogFileName = "got_err.log"
)

var (
	logger      *zap.Logger
	closeLogger = func() error { return nil }
)

// InitLogger init logger.
func InitLogger() {
	_ = closeLogger()
	logger, closeLogger = NewLogger()
	zap.ReplaceGlobals(logger)
}

// NewLogger builds a logger and a function that closes its rotating log files.
func NewLogger() (*zap.Logger, func() error) {
	// create log dir if not exists
	if config.BotConfig.LogFileDir != "" {
		if err := os.MkdirAll(config.BotConfig.LogFileDir, 0755); err != nil {
			zap.L().Fatal("Create log dir failed", zap.Error(err))
		}
	}
	if config.BotConfig.DebugMode {
		return buildLogger(devConfig())
	}
	return buildLogger(prodConfig())
}

func buildLogger(cfg zap.Config) (*zap.Logger, func() error) {
	output, outFile := outputSyncer(logFileName)
	errOutput, errFile := outputSyncer(errLogFileName)
	encoder := zapcore.NewJSONEncoder(cfg.EncoderConfig)
	core := zapcore.NewCore(encoder, output, cfg.Level)
	if cfg.Sampling != nil {
		core = zapcore.NewSamplerWithOptions(core, time.Second, cfg.Sampling.Initial, cfg.Sampling.Thereafter)
	}
	opts := []zap.Option{
		zap.ErrorOutput(errOutput),
		zap.AddCaller(),
		zap.AddCallerSkip(1),
	}
	if cfg.Development {
		opts = append(opts, zap.Development(), zap.AddStacktrace(zapcore.WarnLevel))
	} else {
		opts = append(opts, zap.AddStacktrace(zapcore.ErrorLevel))
	}
	return zap.New(core, opts...), closeFiles(lo.Compact([]io.Closer{outFile, errFile}))
}

func closeFiles(files []io.Closer) func() error {
	var once sync.Once
	var err error
	return func() error {
		once.Do(func() {
			err = errors.Join(lo.Map(files, func(f io.Closer, _ int) error { return f.Close() })...)
		})
		return err
	}
}

func outputSyncer(fileName string) (zapcore.WriteSyncer, io.Closer) {
	dir := config.BotConfig.LogFileDir
	if dir == "" {
		return stderrSyncer{}, nil
	}
	file := rotatingFile(filepath.Join(dir, fileName), config.BotConfig.LogConfig)
	return zapcore.NewMultiWriteSyncer(stderrSyncer{}, zapcore.AddSync(file)), file
}

func rotatingFile(path string, cfg *config.LogConfig) *lumberjack.Logger {
	out := &lumberjack.Logger{Filename: path, LocalTime: true, Compress: cfg.CompressEnabled()}
	if cfg != nil {
		out.MaxSize = cfg.MaxSizeMB
		out.MaxBackups = cfg.MaxBackups
		out.MaxAge = cfg.MaxAgeDays
	}
	return out
}

type stderrSyncer struct{}

func (stderrSyncer) Write(p []byte) (int, error) {
	return os.Stderr.Write(p)
}

func (stderrSyncer) Sync() error {
	return ignoreUnsyncable(os.Stderr.Sync())
}

func ignoreUnsyncable(err error) error {
	if errors.Is(err, syscall.EINVAL) || errors.Is(err, syscall.ENOTTY) || errors.Is(err, syscall.EBADF) {
		return nil
	}
	return err
}

func devConfig() zap.Config {
	return zap.Config{
		Level:       zap.NewAtomicLevelAt(zap.DebugLevel),
		Development: true,
		Encoding:    "json",
		EncoderConfig: zapcore.EncoderConfig{
			TimeKey:        "ts",
			LevelKey:       "level",
			NameKey:        "logger",
			CallerKey:      "caller",
			FunctionKey:    zapcore.OmitKey,
			MessageKey:     "msg",
			StacktraceKey:  "stacktrace",
			LineEnding:     zapcore.DefaultLineEnding,
			EncodeLevel:    zapcore.CapitalLevelEncoder,
			EncodeTime:     zapcore.ISO8601TimeEncoder,
			EncodeDuration: zapcore.StringDurationEncoder,
			EncodeCaller:   zapcore.ShortCallerEncoder,
		},
	}
}

func prodConfig() zap.Config {
	return zap.Config{
		Level:       zap.NewAtomicLevelAt(zap.InfoLevel),
		Development: false,
		Sampling: &zap.SamplingConfig{
			Initial:    100,
			Thereafter: 100,
		},
		Encoding: "json",
		EncoderConfig: zapcore.EncoderConfig{
			TimeKey:        "ts",
			LevelKey:       "level",
			NameKey:        "logger",
			CallerKey:      "caller",
			FunctionKey:    zapcore.OmitKey,
			MessageKey:     "msg",
			StacktraceKey:  "stacktrace",
			LineEnding:     zapcore.DefaultLineEnding,
			EncodeLevel:    zapcore.CapitalLevelEncoder,
			EncodeTime:     zapcore.EpochTimeEncoder,
			EncodeDuration: zapcore.SecondsDurationEncoder,
			EncodeCaller:   zapcore.ShortCallerEncoder,
		},
	}
}

// Debug print log at debug level.
func Debug(msg string, fields ...zap.Field) {
	logger.Debug(msg, fields...)
}

// Info print log at info level.
func Info(msg string, fields ...zap.Field) {
	logger.Info(msg, fields...)
}

// Warn print log at warning level.
func Warn(msg string, fields ...zap.Field) {
	logger.Warn(msg, fields...)
}

// Error print log at error level.
func Error(msg string, fields ...zap.Field) {
	logger.Error(msg, fields...)
}

// Fatal print log at fatal level, then calls os.Exit(1).
func Fatal(msg string, fields ...zap.Field) {
	logger.Fatal(msg, fields...)
}

// Panic print log at panic level, then panic.
func Panic(msg string, fields ...zap.Field) {
	logger.Panic(msg, fields...)
}

// Sync sync logger.
func Sync() {
	if err := ignoreUnsyncable(logger.Sync()); err != nil {
		logger.Error("Logger Sync failed", zap.Error(err))
	}
}

// Close syncs the logger and closes its rotating log files.
func Close() {
	Sync()
	if err := closeLogger(); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "Logger Close failed:", err)
	}
}
