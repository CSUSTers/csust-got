package store

import (
	"csust-got/log"
	"csust-got/orm"
	"csust-got/util"
	"errors"
	"time"

	"go.uber.org/zap"
)

// TaskDeadTime is the time how long the expired task can live.
// Task will be deleted when bot started if task is expired for TaskDeadTime.
const TaskDeadTime = time.Hour * 6 // 6h
// FetchTaskTime fetch the task in future.
const FetchTaskTime = time.Minute // 1min

// Task is an alias of orm.Task.
type Task = orm.Task

// RawTask is an alias of orm.RawTask.
type RawTask = orm.RawTask

// TaskNonced is an alias of orm.TaskNonced.
type TaskNonced = orm.TaskNonced

// TimeTask is a time task runner.
type TimeTask struct {
	nextTime util.RWMutexed[int64]

	fn func(task *Task)

	// add task to this channel,
	// it will be add to redis or add scheduler directly depending on execTime.
	addChan chan *Task
	// the tasks in this channel will be deleted from redis.
	deleteChan chan *RawTask
	// the tasks in this channel will be added to scheduler directly.
	runningChan chan *Task
	// the tasks in this channel will be added to scheduler, and deleted from redis.
	toRunChan chan *RawTask
}

// NewTimeTask creates a new time task runner.
func NewTimeTask(fn func(task *Task)) *TimeTask {
	return &TimeTask{
		fn:          fn,
		addChan:     make(chan *Task, 64),
		deleteChan:  make(chan *RawTask, 64),
		runningChan: make(chan *Task, 64),
		toRunChan:   make(chan *RawTask, 64),
	}
}

// RunTaskFn returns function to add task to scheduler.
func (t *TimeTask) RunTaskFn(task *Task) func() {
	return func() {
		t.fn(task)
	}
}

// RunTaskAndDeleteFn returns function to add task to scheduler, and delete from redis after finished.
func (t *TimeTask) RunTaskAndDeleteFn(task *RawTask) func() {
	return func() {
		t.fn(&task.Task)
		t.DeleteTask(task)
	}
}

// Run start running loop.
func (t *TimeTask) Run() {
	const maxTries = 16
	const maxIllTime = time.Second * 16

	waiter := make(chan string, 1)

	// start loops
	go t.getLoopFn("add_loop", waiter)()
	go t.getLoopFn("delete_loop", waiter)()
	go t.getLoopFn("running_loop", waiter)()
	go t.getLoopFn("fetch_loop", waiter)()

	tries := 0
	var timer <-chan time.Time

	for tries < maxTries {
		select {
		case exited := <-waiter:
			if timer == nil {
				timer = time.After(maxIllTime)
			}
			log.Error("time task loop exited", zap.String("loop", exited))
			go t.getLoopFn(exited, waiter)()
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
		// if the mix time is before t.nextTime or t.nextTime <= 0, replace the nextTime.
		nextTime := t.nextTime.Get()
		if minTime < nextTime || nextTime <= 0 {
			t.nextTime.Set(minTime)
		}
		tasks = tasks[:0]
		timer.Reset(time.Second)
	}
}

func (t *TimeTask) parseTasks(tasks []*orm.Task) ([]*orm.TaskNonced, int64) {
	ts := make([]*TaskNonced, 0, len(tasks))
	minTime := t.nextTime.Get()
	for _, task := range tasks {
		now := time.Now()
		if task.ExecTime < now.Add(FetchTaskTime).UnixMilli() {
			t.runningChan <- task
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
	const timerCycleTime = time.Second * 15
	const tryCycleTime = time.Microsecond * 10

	taskStrs := make([]string, 0, 8)
	timer := time.NewTimer(timerCycleTime)

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
			timer.Reset(tryCycleTime)
		} else {
			taskStrs = taskStrs[:0]
			timer.Reset(timerCycleTime)
		}
	}
}

func (t *TimeTask) runningTaskLoop() {
	for {
		select {
		case task := <-t.runningChan:
			time.AfterFunc(time.Until(time.UnixMilli(task.ExecTime)), t.RunTaskFn(task))
		case task := <-t.toRunChan:
			time.AfterFunc(time.Until(time.UnixMilli(task.ExecTime)), t.RunTaskAndDeleteFn(task))
		}
	}
}

func (t *TimeTask) fetchTaskLoop() {
	ticker := time.NewTicker(time.Second)
	for range ticker.C {
		startTime := t.nextTime.Get()
		endTime := time.Now().Add(FetchTaskTime).UnixMilli()

		// fetch tasks from redis, and add to toRunChan
		err := t.fetchTask(startTime, endTime)
		if err != nil {
			if !errors.Is(err, orm.ErrNoTask) {
				log.Error("query tasks error", zap.Error(err))
			}
		}
	}
}

func (t *TimeTask) fetchTask(from, to int64) error {
	// fetch tasks from redis, and add to toRunChan
	ts, err := orm.QueryTasks(from, to)
	if err != nil {
		return err
	}

	for _, task := range ts {
		t.toRunChan <- task
	}

	// fetch next time from redis
	next, err := orm.NextTaskTime(to)
	if err != nil {
		return err
	}
	t.nextTime.Set(next)
	return nil
}

func (t *TimeTask) getLoopFn(name string, waiter chan string) func() {
	switch name {
	case "add_loop":
		return func() {
			t.addTaskLoop()
			waiter <- "add_loop"
		}

	case "delete_loop":
		return func() {
			t.deleteTaskLoop()
			waiter <- "delete_loop"
		}
	case "running_loop":
		return func() {
			t.runningTaskLoop()
			waiter <- "running_loop"
		}
	case "fetch_loop":
		return func() {
			t.fetchTaskLoop()
			waiter <- "fetch_loop"
		}
	default:
		panic("unknown loop name")
	}
}
