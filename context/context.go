package context

import (
	"context"
	"csust-got/config"
	"fmt"
	"github.com/go-redis/redis/v7"
	"math/rand"
	"time"
)

type Task func()
type TaskID int64
type TaskInfo struct {
	ID     TaskID
	Name   string
	Cancel context.CancelFunc
}

func NewRandomKey() int64 {
	return rand.Int63()
}

type Context struct {
	namespace    string
	globalClient *redis.Client
	globalConfig *config.Config
	executeCtx   context.Context
	runningTasks map[TaskID]TaskInfo
}

func (ctx Context) GlobalClient() *redis.Client {
	return ctx.globalClient
}

func (ctx Context) GlobalConfig() *config.Config {
	return ctx.globalConfig
}

func (ctx Context) WrapKey(key string) string {
	return fmt.Sprintf("%s:%s", ctx.namespace, key)
}

func (ctx Context) DoAfterNamed(task Task, delay time.Duration, name string) TaskID {
	sub, cancel := context.WithCancel(ctx.executeCtx)
	id := TaskID(NewRandomKey())
	for _, ok := ctx.runningTasks[id]; ok; {
		id = TaskID(NewRandomKey())
	}
	t := TaskInfo{
		ID:     id,
		Cancel: cancel,
		Name:   name,
	}
	ctx.runningTasks[id] = t
	taskRun := func() {
		tick := time.NewTicker(delay)
		defer tick.Stop()
		select {
		case <-sub.Done():
			return
		case <-tick.C:
			task()
		}
	}
	go taskRun()
	return id
}

func (ctx Context) CancelTask(id TaskID) {
	if task, ok := ctx.runningTasks[id]; ok {
		task.Cancel()
	}
}

func (ctx Context) SubContext(sub string) Context {
	return Context{
		ctx.WrapKey(sub),
		ctx.globalClient,
		ctx.globalConfig,
		ctx.executeCtx,
		ctx.runningTasks,
	}
}

func Global(globalClient *redis.Client, globalConfig *config.Config) Context {
	return Context{
		namespace:    "",
		globalClient: globalClient,
		globalConfig: globalConfig,
		executeCtx:   context.Background(),
		runningTasks: make(map[TaskID]TaskInfo),
	}
}
