package context

import (
	"csust-got/config"
	"fmt"
	"github.com/go-redis/redis/v7"
	"math/rand"
	"time"
)

const (
	ConstChatID   = "chatID"
	ConstChatName = "chatName"
	ConstMessage  = "message"
)

type Task func()
type TaskID int64
type TaskInfo struct {
	ID     TaskID
	Name   string
	Cancel *time.Timer
}

func NewRandomKey() int64 {
	return rand.Int63()
}

type Context struct {
	namespace    string
	globalClient *redis.Client
	globalConfig *config.Config
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
	id := TaskID(NewRandomKey())
	for _, ok := ctx.runningTasks[id]; ok; {
		id = TaskID(NewRandomKey())
	}
	t := TaskInfo{
		ID:     id,
		Cancel: time.AfterFunc(delay, task),
		Name:   name,
	}
	ctx.runningTasks[id] = t
	return id
}

func (ctx Context) CancelTask(id TaskID) {
	if task, ok := ctx.runningTasks[id]; ok {
		task.Cancel.Stop()
	}
}

func (ctx Context) SubContext(sub string) Context {
	return Context{
		ctx.WrapKey(sub),
		ctx.globalClient,
		ctx.globalConfig,
		ctx.runningTasks,
	}
}

func Global(globalClient *redis.Client, globalConfig *config.Config) Context {
	return Context{
		namespace:    "",
		globalClient: globalClient,
		globalConfig: globalConfig,
		runningTasks: make(map[TaskID]TaskInfo),
	}
}
