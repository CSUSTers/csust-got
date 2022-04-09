package store

import (
	"csust-got/log"
	"csust-got/orm"
	"time"

	"go.uber.org/zap"
)

// InitTimeTaskStore initializes the time task.
func InitTimeTaskStore() {
	tasks, err := orm.QueryTasks(0, time.Now().Add(FetchTaskTime).Unix())
	if err != nil {
		log.Error("init query tasks error", zap.Error(err))
		return
	}
	now := time.Now()
	ddl := now.Add(TaskDeadTime).UnixMilli()
	for _, t := range tasks {
		if t.ExecTime < ddl {
			log.Info("task exec time expired, skip it", zap.String("task", t.Raw))
			continue
		}
		// TODO: add task to runner
	}
}
