package store

import (
	"testing"
	"time"

	"csust-got/orm"

	"github.com/stretchr/testify/require"
	. "gopkg.in/telebot.v3"
)

func TestParseTasksUpdatesNextTimeForFutureTasks(t *testing.T) {
	runner := NewTimeTask(func(*Task) {})
	now := time.Now()
	initialNext := now.Add(10 * time.Minute).UnixMilli()
	runner.nextTime.LockSet(initialNext)

	soonTask := &orm.Task{ExecTime: now.Add(10 * time.Second).UnixMilli()}
	futureTask := &orm.Task{ExecTime: now.Add(5 * time.Minute).UnixMilli()}
	laterTask := &orm.Task{ExecTime: now.Add(20 * time.Minute).UnixMilli()}

	ts, next := runner.parseTasks([]*orm.Task{soonTask, futureTask, laterTask})

	require.Len(t, runner.runningChan, 1)
	require.Equal(t, soonTask, <-runner.runningChan)
	require.Len(t, ts, 2)
	require.Equal(t, futureTask.ExecTime, next)
}

func TestDeleteMsgQueueProcessNilSafe(t *testing.T) {
	q := &DeleteMsgQueue{}
	require.NoError(t, q.process(nil))
	require.NoError(t, q.process(&Message{ID: 1}))
}
