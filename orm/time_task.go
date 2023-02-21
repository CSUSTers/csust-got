package orm

import (
	"context"
	"csust-got/log"
	"csust-got/util"
	"encoding/json"
	"errors"

	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
)

// TimeTaskKeyBody is the redis key for time task.
const TimeTaskKeyBody = "TIME_TASK_SET"

var (
	// timeTaskKey *string

	// ErrNoTask means no task in redis.
	ErrNoTask = errors.New("no task")
)

// TimeTaskKey returns the redis key for time task, it will initialize the key at first running.
func TimeTaskKey() string {
	return wrapKey(TimeTaskKeyBody)
}

// Task is a struct stores the task info.
type Task struct {
	User   string `json:"u"`
	UserId int64  `json:"uid"`
	ChatId int64  `json:"cid"`
	Info   string `json:"i"`

	// ExecTime is the time when the task will be executed, MilliSecond and UTC.
	ExecTime int64 `json:"et"`
	// SetTime is the time when the task is added, MilliSecond and UTC.
	SetTime int64 `json:"st"`
}

// TaskNonced is Task with nonce.
type TaskNonced struct {
	*Task

	// Nonce is a random string to make sure the uniqueness of the task.
	Nonce []byte `json:"n"`
}

// RawTask is Task with serialized raw string.
type RawTask struct {
	Task

	Raw string `json:"raw"`
}

// NewTaskNonced return a TaskNonced with nonce.
func NewTaskNonced(t *Task) *TaskNonced {
	return &TaskNonced{
		Task:  t,
		Nonce: util.RandBytes(),
	}
}

// AddTasks adds tasks to redis.
func AddTasks(tasks ...*TaskNonced) error {
	if len(tasks) == 0 {
		return nil
	}

	zs := make([]redis.Z, 0, len(tasks))
	for _, t := range tasks {
		value, err := json.Marshal(t)
		if err != nil {
			log.Error("json marshal failed", zap.Error(err), zap.Any("task", t))
			return err
		}
		zs = append(zs, redis.Z{
			Score:  float64(t.ExecTime),
			Member: value,
		})
	}

	err := rc.ZAdd(context.TODO(), TimeTaskKey(), zs...).Err()
	return err
}

// QueryTasks query tasks from redis with a time range.
func QueryTasks(from, to int64) ([]*RawTask, error) {
	froms, tos := util.I2Dec(from), util.I2Dec(to)
	zs, err := rc.ZRangeByScore(context.TODO(), TimeTaskKey(), &redis.ZRangeBy{
		Min: froms,
		Max: tos,
	}).Result()
	if err != nil {
		log.Error("query tasks failed", zap.Error(err))
		return nil, err
	}

	tasks := make([]*RawTask, 0, len(zs))
	for _, z := range zs {
		var t RawTask
		err := json.Unmarshal([]byte(z), &t.Task)
		if err != nil {
			log.Error("json unmarshal failed", zap.Error(err), zap.String("task", z))
			return nil, err
		}
		t.Raw = z
		tasks = append(tasks, &t)
	}

	return tasks, nil
}

// DelTasksInTimeRange deletes tasks from redis with time range, and returns the next task score.
func DelTasksInTimeRange(from, to int64) (next float64, err error) {
	froms, tos := util.I2Dec(from), util.I2Dec(to)
	err = rc.ZRemRangeByScore(context.TODO(), wrapKey(TimeTaskKeyBody), froms, tos).Err()
	if err != nil {
		log.Error("del tasks failed", zap.Error(err))
		return 0, err
	}

	zs, err := rc.ZRangeByScoreWithScores(context.TODO(), TimeTaskKey(), &redis.ZRangeBy{
		Min:   "(" + tos,
		Max:   "inf",
		Count: 1,
	}).Result()
	if err != nil {
		log.Error("get tasks count failed", zap.Error(err))
		return 0, err
	}

	if len(zs) == 0 {
		return 0, nil
	}
	return zs[0].Score, err
}

// NextTaskTime returns the next task time.
func NextTaskTime(start int64) (int64, error) {
	zs, err := rc.ZRangeByScoreWithScores(context.TODO(), TimeTaskKey(), &redis.ZRangeBy{
		Min:   "(" + util.I2Dec(start),
		Max:   "inf",
		Count: 1,
	}).Result()
	if err != nil {
		log.Error("get tasks count failed", zap.Error(err))
		return 0, err
	}
	if len(zs) == 0 {
		return 0, ErrNoTask
	}
	return int64(zs[0].Score), nil
}

// DeleteTasks deletes a task from redis.
func DeleteTasks(raws ...string) error {
	if len(raws) == 0 {
		return nil
	}
	is := make([]interface{}, 0, len(raws))
	for _, r := range raws {
		is = append(is, r)
	}
	return rc.ZRem(context.TODO(), TimeTaskKey(), is...).Err()
}
