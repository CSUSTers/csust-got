package util

import (
	"csust-got/context"
	"github.com/go-redis/redis/v7"
)

func GetBool(ctx context.Context, key string) (bool, error) {
	enable, err := ctx.GlobalClient().Get(ctx.WrapKey(key)).Int()
	if err == redis.Nil {
		return false, nil
	}
	enabled := enable > 0
	return enabled, err
}

func WriteBool(ctx context.Context, key string, value bool) error {
	newI := 0
	if value {
		newI = 1
	}
	return ctx.GlobalClient().Set(ctx.WrapKey(key), newI, 0).Err()
}

func ToggleBool(ctx context.Context, key string) error {
	b, err := GetBool(ctx, key)
	if err != nil {
		return err
	}
	return WriteBool(ctx, key, !b)
}
