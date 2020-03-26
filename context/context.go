package context

import (
	"csust-got/config"
	"fmt"
	"github.com/go-redis/redis/v7"
	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api"
	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/checker/decls"
	"math/rand"
	"time"
)

const (
	ConstChatID   = "chatID"
	ConstChatName = "chatName"
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
	cel          *cel.Env
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

func EvalCELWithVals(env *cel.Env, prog string, vals map[string]interface{}) (interface{}, error) {
	parsed, issue := env.Parse(prog)
	if issue != nil {
		return nil, issue.Err()
	}
	checked, issue := env.Check(parsed)
	if issue != nil {
		return nil, issue.Err()
	}
	program, err := env.Program(checked)
	if err != nil {
		return nil, err
	}
	result, _, err := program.Eval(vals)
	if err != nil {
		return nil, err
	}
	return result.Value(), nil
}

func (ctx Context) EvalCEL(cel string, msg *tgbotapi.Message) (interface{}, error) {
	return EvalCELWithVals(ctx.cel, cel, map[string]interface{}{
		ConstChatID:   msg.Chat.ID,
		ConstChatName: fmt.Sprintf("%s", msg.Chat.Title),
	})
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
		ctx.cel,
	}
}

func Global(globalClient *redis.Client, globalConfig *config.Config) Context {
	env, _ := cel.NewEnv(cel.Declarations(
		decls.NewIdent(ConstChatID, decls.Int, nil),
		decls.NewIdent(ConstChatName, decls.String, nil)))
	return Context{
		namespace:    "",
		globalClient: globalClient,
		globalConfig: globalConfig,
		runningTasks: make(map[TaskID]TaskInfo),
		cel:          env,
	}
}
