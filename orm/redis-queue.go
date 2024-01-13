package orm

import (
	"context"
	"encoding/json"
	"github.com/redis/go-redis/v9"
)

func PushQueue(key string, value interface{}, score int64) error {
	member, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return rc.ZAdd(context.Background(), wrapKey(key), redis.Z{
		Score:  float64(score),
		Member: string(member),
	}).Err()
}

func RemoveFromQueue(key string, value interface{}) error {
	member, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return rc.ZRem(context.Background(), wrapKey(key), string(member)).Err()
}

const (
	popQueueScript = `
local res = redis.call('ZRANGEBYSCORE', KEYS[1], ARGV[1], ARGV[2])
if #res > 0 then
	redis.call('ZREM', KEYS[1], unpack(res))
end
return res
`
)

func PopQueue(key string, from, to int64) ([]any, error) {
	z, err := rc.Eval(context.Background(), popQueueScript, []string{wrapKey(key)}, from, to).Result()
	if err != nil {
		return nil, err
	}
	return z.([]any), nil
}
