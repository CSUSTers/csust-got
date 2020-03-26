package util

import (
	"csust-got/context"
	"github.com/go-redis/redis/v7"
)

// GetBool gets a bool type value to a key in the redis storage.
// This function will call WrapKey, so you needn't warp your key.
func GetBool(ctx context.Context, key string) (bool, error) {
	enable, err := ctx.GlobalClient().Get(ctx.WrapKey(key)).Int()
	if err == redis.Nil {
		return false, nil
	}
	enabled := enable > 0
	return enabled, err
}

// WriteBool writes a bool type value to a key in the redis storage.
// This function will call WrapKey, so you needn't warp your key.
func WriteBool(ctx context.Context, key string, value bool) error {
	newI := 0
	if value {
		newI = 1
	}
	return ctx.GlobalClient().Set(ctx.WrapKey(key), newI, 0).Err()
}

// ToggleBool toggles(negative) a bool type value to a key in the redis storage.
// This function will call WrapKey, so you needn't warp your key.
func ToggleBool(ctx context.Context, key string) error {
	b, err := GetBool(ctx, key)
	if err != nil {
		return err
	}
	return WriteBool(ctx, key, !b)
}
