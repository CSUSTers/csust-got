package context

import (
	"csust-got/config"
	"fmt"
	"github.com/go-redis/redis/v7"
)

type Context struct {
	namespace    string
	globalClient *redis.Client
	globalConfig *config.Config
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

func (ctx Context) SubContext(sub string) Context {
	return Context{
		ctx.WrapKey(sub),
		ctx.globalClient,
		ctx.globalConfig,
	}
}

func Global(globalClient *redis.Client, globalConfig *config.Config) Context {
	return Context{
		namespace:    "",
		globalClient: globalClient,
		globalConfig: globalConfig,
	}
}
