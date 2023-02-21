package orm

import (
	"context"
	"errors"
	"time"

	"github.com/redis/go-redis/v9"
)

// GetBool gets a bool type value to a key in the redis storage.
// This function will call WrapKey, so you needn't warp your key.
func GetBool(key string) (bool, error) {
	// TODO: replace ctx with real ctx
	enable, err := rc.Get(context.TODO(), key).Int()
	if errors.Is(err, redis.Nil) {
		return false, nil
	}
	return enable > 0, err
}

// WriteBool writes a bool type value to a key in the redis storage.
// This function will call WrapKey, so you needn't warp your key.
func WriteBool(key string, value bool, expiration time.Duration) error {
	newI := 0
	if value {
		newI = 1
	}
	// TODO: replace ctx with real ctx
	return rc.Set(context.TODO(), key, newI, expiration).Err()
}

// ToggleBool toggles(negative) a bool type value to a key in the redis storage.
// This function will call WrapKey, so you needn't warp your key.
func ToggleBool(key string) error {
	b, err := GetBool(key)
	if err != nil {
		return err
	}
	return WriteBool(key, !b, 0)
}

// GetTTL get key expire duration.
func GetTTL(key string) (time.Duration, error) {
	// TODO: replace ctx with real ctx
	sec, err := rc.TTL(context.TODO(), key).Result()
	if err != nil || sec < 0 {
		return 0, err
	}
	return sec, nil
}
