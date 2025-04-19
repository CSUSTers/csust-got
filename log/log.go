package log

import (
	"csust-got/config"
	"os"

	"github.com/samber/lo"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

var logger *zap.Logger

// InitLogger init logger.
func InitLogger() {
	logger = NewLogger()
	zap.ReplaceGlobals(logger)
}

// NewLogger new logger.
func NewLogger() *zap.Logger {
	var logConfig zap.Config
	// create log dir if not exists
	if config.BotConfig.LogFileDir != "" {
		if err := os.MkdirAll(config.BotConfig.LogFileDir, 0755); err != nil {
			zap.L().Fatal("Create log dir failed", zap.Error(err))
		}
	}
	if config.BotConfig.DebugMode {
		logConfig = devConfig()
	} else {
		logConfig = prodConfig()
	}
	tmpLogger, err := logConfig.Build(zap.AddCallerSkip(1))
	if err == nil {
		return tmpLogger
	}
	zap.L().Error("NewLogger failed, using default logger", zap.Error(err))
	return zap.L()
}

func devConfig() zap.Config {
	logPath := lo.FilterMap([]string{config.BotConfig.LogFileDir}, func(p string, _ int) (string, bool) {
		return p, p != ""
	})
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
		OutputPaths:      append(lo.Map(logPath, func(p string, _ int) string { return p + "/got.log" }), "stderr"),
		ErrorOutputPaths: append(lo.Map(logPath, func(p string, _ int) string { return p + "/got_err.log" }), "stderr"),
	}
}

func prodConfig() zap.Config {
	logPath := lo.FilterMap([]string{config.BotConfig.LogFileDir}, func(p string, _ int) (string, bool) {
		return p, p != ""
	})
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
		OutputPaths:      append(lo.Map(logPath, func(p string, _ int) string { return p + "/got.log" }), "stderr"),
		ErrorOutputPaths: append(lo.Map(logPath, func(p string, _ int) string { return p + "/got_err.log" }), "stderr"),
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
	if err := logger.Sync(); err != nil {
		logger.Error("Logger Sync failed", zap.Error(err))
	}
}
