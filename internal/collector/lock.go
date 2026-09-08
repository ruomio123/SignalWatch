package collector

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"time"

	goredis "github.com/redis/go-redis/v9"
)

const releaseLockScript = `
if redis.call('GET', KEYS[1]) == ARGV[1] then
  return redis.call('DEL', KEYS[1])
end
return 0
`

type RedisCommands interface {
	SetNX(ctx context.Context, key string, value any, expiration time.Duration) *goredis.BoolCmd
	Eval(ctx context.Context, script string, keys []string, args ...any) *goredis.Cmd
}

type ReleaseFunc func(context.Context) error

type LockManager interface {
	Acquire(ctx context.Context, ttl time.Duration) (ReleaseFunc, bool, error)
}

type RedisLockManager struct {
	client RedisCommands
	key    string
}

func NewRedisLockManager(client RedisCommands, key string) (*RedisLockManager, error) {
	if client == nil || key == "" {
		return nil, errors.New("invalid collector lock configuration")
	}
	return &RedisLockManager{client: client, key: key}, nil
}

func (manager *RedisLockManager) Acquire(
	ctx context.Context,
	ttl time.Duration,
) (ReleaseFunc, bool, error) {
	if ttl <= 0 {
		return nil, false, errors.New("invalid collector lock request")
	}
	tokenBytes := make([]byte, 16)
	if _, err := rand.Read(tokenBytes); err != nil {
		return nil, false, err
	}
	token := hex.EncodeToString(tokenBytes)
	acquired, err := manager.client.SetNX(ctx, manager.key, token, ttl).Result()
	if err != nil || !acquired {
		return nil, acquired, err
	}
	release := func(releaseContext context.Context) error {
		return manager.client.Eval(
			releaseContext,
			releaseLockScript,
			[]string{manager.key},
			token,
		).Err()
	}
	return release, true, nil
}
