package orm

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/redis/go-redis/v9"
)

// PushQueue pushes sth to a queue
func PushQueue(key string, value any, score int64) error {
	member, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return rc.ZAdd(context.Background(), wrapKey(key), redis.Z{
		Score:  float64(score),
		Member: string(member),
	}).Err()
}

// RemoveFromQueue removes sth from a queue
func RemoveFromQueue(key string, value any) error {
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

// PopQueue pops sth from a queue
func PopQueue(key string, from, to int64) ([]string, error) {
	z, err := rc.Eval(context.Background(), popQueueScript, []string{wrapKey(key)}, from, to).Result()
	if err != nil {
		return nil, err
	}
	if z == nil {
		return nil, nil
	}
	zs, ok := z.([]any)
	if !ok {
		return nil, fmt.Errorf("[PopQueue] %w, invalid result type: %T", ErrWrongType, z)
	}
	res := make([]string, 0, len(zs))
	for _, z := range zs {
		res = append(res, z.(string))
	}
	return res, nil
}
