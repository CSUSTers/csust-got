package log

import "go.uber.org/zap"

type botLogger zap.SugaredLogger

func (log botLogger) Println(v ...interface{}) {
	logger.Sugar().Info(v...)
}

func (log botLogger) Printf(format string, v ...interface{}) {
	logger.Sugar().Infof(format, v...)
}
