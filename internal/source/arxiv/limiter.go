package arxiv

import (
	"context"
	"errors"
	"time"

	goredis "github.com/redis/go-redis/v9"
)

const limiterScript = `
local current = redis.call('TIME')
local now_ms = current[1] * 1000 + math.floor(current[2] / 1000)
local next_ms = tonumber(redis.call('GET', KEYS[1]) or '0')
local reserved_ms = math.max(now_ms, next_ms)
local wait_ms = reserved_ms - now_ms
redis.call('SET', KEYS[1], reserved_ms + ARGV[1], 'PX', wait_ms + ARGV[2])
return wait_ms
`

type ScriptRunner interface {
	Eval(ctx context.Context, script string, keys []string, args ...any) *goredis.Cmd
}

type RedisLimiter struct {
	client   ScriptRunner
	key      string
	interval time.Duration
}

func NewRedisLimiter(client ScriptRunner, key string, interval time.Duration) (*RedisLimiter, error) {
	if client == nil || key == "" || interval < 3*time.Second {
		return nil, errors.New("invalid arxiv limiter configuration")
	}
	return &RedisLimiter{client: client, key: key, interval: interval}, nil
}

func (limiter *RedisLimiter) Wait(ctx context.Context) error {
	ttl := 10 * limiter.interval
	result := limiter.client.Eval(
		ctx,
		limiterScript,
		[]string{limiter.key},
		limiter.interval.Milliseconds(),
		ttl.Milliseconds(),
	)
	delayMilliseconds, err := result.Int64()
	if err != nil {
		return err
	}
	if delayMilliseconds <= 0 {
		return nil
	}
	timer := time.NewTimer(time.Duration(delayMilliseconds) * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
