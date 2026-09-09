package digest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	goredis "github.com/redis/go-redis/v9"
)

const releaseUserLockScript = `
if redis.call('GET', KEYS[1]) == ARGV[1] then
  return redis.call('DEL', KEYS[1])
end
return 0
`

type RedisCommands interface {
	SetNX(ctx context.Context, key string, value any, expiration time.Duration) *goredis.BoolCmd
	Exists(ctx context.Context, keys ...string) *goredis.IntCmd
	Set(ctx context.Context, key string, value any, expiration time.Duration) *goredis.StatusCmd
	Eval(ctx context.Context, script string, keys []string, args ...any) *goredis.Cmd
}

type ReleaseFunc func(context.Context) error

type Coordinator interface {
	Acquire(ctx context.Context, job Job) (ReleaseFunc, bool, error)
	IsComplete(ctx context.Context, job Job) (bool, error)
	MarkComplete(ctx context.Context, job Job) error
}

type RedisCoordinator struct {
	client        RedisCommands
	prefix        string
	lockTTL       time.Duration
	completionTTL time.Duration
}

func NewRedisCoordinator(
	client RedisCommands,
	prefix string,
	lockTTL time.Duration,
	completionTTL time.Duration,
) (*RedisCoordinator, error) {
	if client == nil || prefix == "" || lockTTL <= 0 || completionTTL <= 0 {
		return nil, errors.New("invalid digest coordinator configuration")
	}
	return &RedisCoordinator{
		client: client, prefix: prefix, lockTTL: lockTTL, completionTTL: completionTTL,
	}, nil
}

func (coordinator *RedisCoordinator) Acquire(
	ctx context.Context,
	job Job,
) (ReleaseFunc, bool, error) {
	if err := validateJob(job); err != nil {
		return nil, false, err
	}
	tokenBytes := make([]byte, 16)
	if _, err := rand.Read(tokenBytes); err != nil {
		return nil, false, err
	}
	token := hex.EncodeToString(tokenBytes)
	key := coordinator.lockKey(job)
	acquired, err := coordinator.client.SetNX(ctx, key, token, coordinator.lockTTL).Result()
	if err != nil || !acquired {
		return nil, acquired, err
	}
	return func(releaseContext context.Context) error {
		return coordinator.client.Eval(
			releaseContext, releaseUserLockScript, []string{key}, token,
		).Err()
	}, true, nil
}

func (coordinator *RedisCoordinator) IsComplete(ctx context.Context, job Job) (bool, error) {
	if err := validateJob(job); err != nil {
		return false, err
	}
	count, err := coordinator.client.Exists(ctx, coordinator.doneKey(job)).Result()
	return count > 0, err
}

func (coordinator *RedisCoordinator) MarkComplete(ctx context.Context, job Job) error {
	if err := validateJob(job); err != nil {
		return err
	}
	return coordinator.client.Set(ctx, coordinator.doneKey(job), "1", coordinator.completionTTL).Err()
}

func (coordinator *RedisCoordinator) lockKey(job Job) string {
	return fmt.Sprintf("%s:lock:%d:%s", coordinator.prefix, job.UserID, job.LocalDate)
}

func (coordinator *RedisCoordinator) doneKey(job Job) string {
	return fmt.Sprintf("%s:done:%d:%s", coordinator.prefix, job.UserID, job.LocalDate)
}

func validateJob(job Job) error {
	if job.UserID == 0 {
		return errors.New("invalid digest user id")
	}
	if _, err := time.Parse(localDateLayout, job.LocalDate); err != nil {
		return errors.New("invalid digest local date")
	}
	return nil
}
