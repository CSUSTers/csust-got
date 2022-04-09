package store

import (
	"csust-got/log"
	"csust-got/orm"
	"time"

	"go.uber.org/zap"
)

// TaskDeadTime is the time how long the expired task can live.
// Task will be deleted when bot started if task is expired for TaskDeadTime.
const TaskDeadTime = time.Hour * 24 // 24h
// FetchTaskTime fetch the task in future.
const FetchTaskTime = time.Minute * 5 // 5min

// Task is an alias of orm.Task.
type Task = orm.Task

// RawTask is an alias of orm.RawTask.
type RawTask = orm.RawTask

// TaskNonced is an alias of orm.TaskNonced.
type TaskNonced = orm.TaskNonced

// TimeTask is a time task runner.
type TimeTask struct {
	nextTime int64

	fn func(task *Task)

	addChan    chan *Task
	deleteChan chan *RawTask
}

// NewTimeTask creates a new time task runner.
func NewTimeTask(fn func(task *Task)) *TimeTask {
	return &TimeTask{
		fn:         fn,
		addChan:    make(chan *Task, 64),
		deleteChan: make(chan *RawTask, 64),
	}
}

// RunTaskFn returns function to add task to scheduler.
func (t *TimeTask) RunTaskFn(task *Task, fn func(*Task)) func() {
	return func() {
		fn(task)
	}
}

// RunTaskAndDeleteFn returns function to add task to scheduler, and delete from redis after finished.
func (t *TimeTask) RunTaskAndDeleteFn(task *RawTask, fn func(*Task)) func() {
	return func() {
		fn(&task.Task)
		t.DeleteTask(task)
	}
}

// Run start running loop.
func (t *TimeTask) Run() {
	waiter := make(chan string, 1)
	go func() {
		t.addTaskLoop()
		waiter <- "add_loop"
	}()
	go func() {
		t.deleteTaskLoop()
		waiter <- "delete_loop"
	}()

	const maxTries = 16
	const maxIllTime = time.Second * 16

	tries := 0
	var timer <-chan time.Time

	for tries < maxTries {
		select {
		case exited := <-waiter:
			if timer == nil {
				timer = time.After(maxIllTime)
			}
			log.Error("time task loop exited", zap.String("loop", exited))
			tries++
		case <-timer:
			tries = 0
			timer = nil
			log.Info("time task loop recovered", zap.Int("tries", tries))
		}
	}
	log.Fatal("time task loop exited too many times", zap.Int("tries", tries))
}

// AddTask adds a task to addChan.
func (t *TimeTask) AddTask(task *Task) {
	t.addChan <- task
}

// DeleteTask add a task to deleteChan.
func (t *TimeTask) DeleteTask(task *RawTask) {
	t.deleteChan <- task
}

// nolint: revive // cognitive complexity of this function can not be reduced. 
func (t *TimeTask) addTaskLoop() {
	tasks := make([]*Task, 0, 8)
	timer := time.NewTimer(time.Second)

	for {
	FOR:
		for {
			select {
			case <-timer.C:
				break FOR
			case task := <-t.addChan:
				tasks = append(tasks, task)
				if len(tasks) >= 8 {
					break FOR
				}
			}
		}

		ts, minTime := t.parseTasks(tasks)
		// if add to redis error, then reset timer in 10ms, and try again.
		if err := orm.AddTasks(ts...); err != nil {
			log.Error("add tasks error", zap.Error(err))
			timer.Reset(time.Microsecond * 10)
			continue
		}
		if minTime < t.nextTime || t.nextTime <= 0 {
			t.nextTime = minTime
		}
		tasks = tasks[:0]
		timer.Reset(time.Second)
	}
}

func (t *TimeTask) parseTasks(tasks []*orm.Task) ([]*orm.TaskNonced, int64) {
	ts := make([]*TaskNonced, 0, len(tasks))
	minTime := time.Now().UnixMilli()
	for _, task := range tasks {
		now := time.Now()
		if task.ExecTime < now.Add(FetchTaskTime).UnixMilli() {
			time.AfterFunc(time.Until(time.UnixMilli(task.ExecTime)), t.RunTaskFn(task, t.fn))
		} else {
			if task.ExecTime < minTime {
				minTime = task.ExecTime
			}
			ts = append(ts, orm.NewTaskNonced(task))
		}
	}
	return ts, minTime
}

func (t *TimeTask) deleteTaskLoop() {
	taskStrs := make([]string, 0, 8)
	timer := time.NewTimer(time.Second * 64)

	for {
	FOR:
		for {
			select {
			case task := <-t.deleteChan:
				taskStrs = append(taskStrs, task.Raw)
			case <-timer.C:
				break FOR
			}
		}

		err := orm.DeleteTasks(taskStrs...)
		if err != nil {
			log.Error("delete tasks error", zap.Error(err))
		}
	}
}
