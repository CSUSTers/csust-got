package log

import (
	"csust-got/config"
	"csust-got/prom"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

var logger *zap.Logger

func InitLogger() {
	logger = NewLogger()
	zap.ReplaceGlobals(logger)
}

func NewLogger() *zap.Logger {
	var logConfig zap.Config
	if config.BotConfig.DebugMode {
		logConfig = devConfig()
	} else {
		logConfig = prodConfig()
	}
	if tmpLogger, err := logConfig.Build(zap.AddCallerSkip(1)); err == nil {
		return tmpLogger
	} else {
		zap.L().Error("NewLogger failed, using default logger", zap.Error(err))
		prom.Log(zap.ErrorLevel.String())
	}
	return zap.L()
}

func devConfig() zap.Config {
	return zap.Config{
		Level:       zap.NewAtomicLevelAt(zap.DebugLevel),
		Development: true,
		Encoding:    "console",
		EncoderConfig: zapcore.EncoderConfig{
			TimeKey:        "ts",
			LevelKey:       "level",
			NameKey:        "logger",
			CallerKey:      "caller",
			FunctionKey:    zapcore.OmitKey,
			MessageKey:     "msg",
			StacktraceKey:  "stacktrace",
			LineEnding:     zapcore.DefaultLineEnding,
			EncodeLevel:    zapcore.CapitalColorLevelEncoder,
			EncodeTime:     zapcore.ISO8601TimeEncoder,
			EncodeDuration: zapcore.StringDurationEncoder,
			EncodeCaller:   zapcore.ShortCallerEncoder,
		},
		OutputPaths:      []string{"stderr"},
		ErrorOutputPaths: []string{"stderr"},
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
			EncodeLevel:    zapcore.LowercaseLevelEncoder,
			EncodeTime:     zapcore.EpochTimeEncoder,
			EncodeDuration: zapcore.SecondsDurationEncoder,
			EncodeCaller:   zapcore.ShortCallerEncoder,
		},
		OutputPaths:      []string{"stderr"},
		ErrorOutputPaths: []string{"stderr"},
	}
}

func Debug(msg string, fields ...zap.Field) {
	logger.Debug(msg, fields...)
	prom.Log(zap.DebugLevel.String())
}

func Info(msg string, fields ...zap.Field) {
	logger.Info(msg, fields...)
	prom.Log(zap.InfoLevel.String())
}

func Warn(msg string, fields ...zap.Field) {
	logger.Warn(msg, fields...)
	prom.Log(zap.WarnLevel.String())
}

func Error(msg string, fields ...zap.Field) {
	logger.Error(msg, fields...)
	prom.Log(zap.ErrorLevel.String())
}

func Fatal(msg string, fields ...zap.Field) {
	logger.Fatal(msg, fields...)
	prom.Log(zap.FatalLevel.String())
}

func Panic(msg string, fields ...zap.Field) {
	logger.Panic(msg, fields...)
	prom.Log(zap.PanicLevel.String())
}

func Sync() {
	if err := logger.Sync(); err != nil {
		logger.Error("Logger Sync failed", zap.Error(err))
	}
}
