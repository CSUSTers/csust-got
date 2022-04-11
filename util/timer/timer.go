package timer

import (
	"sort"
	"sync"
)

// TODO: use heap when package `heap` is ready

// Timer is a timer running tasks.
type Timer struct {
	tasks []*Task
	lock  sync.Mutex

	// UTC microsecond
	nextTime int64
}

// Task contains a utc microsecond time and a task function.
type Task struct {
	runAt int64
	task  func()
}

// NewTimer returns a new Timer.
func NewTimer(tasks ...*Task) *Timer {
	if len(tasks) == 0 {
		tasks = make([]*Task, 0)
	} else {
		lessFn := func(i, j int) bool {
			return tasks[i].runAt < tasks[j].runAt
		}
		isSorted := sort.SliceIsSorted(tasks, lessFn)
		if !isSorted {
			sort.Slice(tasks, lessFn)
		}
	}

	return &Timer{
		tasks,
		sync.Mutex{},
		0,
	}
}

// NewTask returns a new Task.
func NewTask(runAt int64, task func()) *Task {
	return &Task{
		runAt,
		task,
	}
}

// nolint:revive // to be refactored
// AddTask adds a task to the timer.
func (t *Timer) AddTask(runAt int64, task func()) {
	t.lock.Lock()
	defer t.lock.Unlock()

	if len(t.tasks) == 0 {
		t.nextTime = runAt
		t.tasks = []*Task{NewTask(runAt, task)}
		return
	}

	if len(t.tasks) == 1 {
		if runAt < t.tasks[0].runAt {
			t.tasks = []*Task{NewTask(runAt, task), t.tasks[0]}
		} else {
			t.tasks = append(t.tasks, NewTask(runAt, task))
		}
		return
	}

	for i := 1; i < len(t.tasks); i++ {
		if t.tasks[i-1].runAt <= runAt && runAt < t.tasks[i].runAt {
			if cap(t.tasks) == len(t.tasks) {
				s := make([]*Task, len(t.tasks)+1)
				copy(s, t.tasks[:i])
				s[i] = NewTask(runAt, task)
				copy(s[i+1:], t.tasks[i:])
				t.tasks = s
			} else {
				t.tasks = t.tasks[:len(t.tasks)+1]
				copy(t.tasks[i+1:len(t.tasks)], t.tasks[i:])
				t.tasks[i] = NewTask(runAt, task)
			}
			return
		}
	}
}
