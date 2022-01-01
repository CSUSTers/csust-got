package orm

import (
	"errors"
	"time"

	"github.com/go-redis/redis/v7"
)

// GetBool gets a bool type value to a key in the redis storage.
// This function will call WrapKey, so you needn't warp your key.
func GetBool(key string) (bool, error) {
	enable, err := client.Get(key).Int()
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
	return client.Set(key, newI, expiration).Err()
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
	sec, err := client.TTL(key).Result()
	if err != nil || sec < 0 {
		return 0, err
	}
	return sec, nil
}
