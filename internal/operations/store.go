package operations

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	goredis "github.com/redis/go-redis/v9"
)

type RedisCommands interface {
	Set(ctx context.Context, key string, value any, expiration time.Duration) *goredis.StatusCmd
	Get(ctx context.Context, key string) *goredis.StringCmd
	MGet(ctx context.Context, keys ...string) *goredis.SliceCmd
	ZAdd(ctx context.Context, key string, members ...goredis.Z) *goredis.IntCmd
	ZRangeByScore(ctx context.Context, key string, opt *goredis.ZRangeBy) *goredis.StringSliceCmd
	ZRemRangeByScore(ctx context.Context, key, min, max string) *goredis.IntCmd
	Expire(ctx context.Context, key string, expiration time.Duration) *goredis.BoolCmd
}

type SnapshotStore interface {
	SaveWorker(ctx context.Context, snapshot WorkerSnapshot) error
	SaveTask(ctx context.Context, snapshot TaskSnapshot) error
	SaveSourceAttempt(ctx context.Context, snapshot SourceAttempt) error
	ListWorkers(ctx context.Context, now time.Time) ([]WorkerSnapshot, error)
	ListTasks(ctx context.Context, now time.Time) ([]TaskSnapshot, error)
	GetSourceAttempt(ctx context.Context, sourceID uint64) (*SourceAttempt, error)
}

type RedisStore struct {
	client    RedisCommands
	prefix    string
	retention time.Duration
}

func NewRedisStore(client RedisCommands, prefix string, retention time.Duration) (*RedisStore, error) {
	if client == nil || prefix == "" || retention < time.Hour {
		return nil, errors.New("invalid operations Redis store configuration")
	}
	return &RedisStore{client: client, prefix: prefix, retention: retention}, nil
}

func (store *RedisStore) SaveWorker(ctx context.Context, snapshot WorkerSnapshot) error {
	return store.saveIndexed(ctx, store.workerIndexKey(), store.workerKey(snapshot.InstanceID), snapshot.LastHeartbeatAt, snapshot)
}

func (store *RedisStore) SaveTask(ctx context.Context, snapshot TaskSnapshot) error {
	key := store.taskKey(snapshot.Task, snapshot.InstanceID)
	return store.saveIndexed(ctx, store.taskIndexKey(), key, snapshot.UpdatedAt, snapshot)
}

func (store *RedisStore) SaveSourceAttempt(ctx context.Context, snapshot SourceAttempt) error {
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		return err
	}
	return store.client.Set(ctx, store.sourceKey(snapshot.SourceID), encoded, store.retention).Err()
}

func (store *RedisStore) ListWorkers(ctx context.Context, now time.Time) ([]WorkerSnapshot, error) {
	keys, err := store.recentKeys(ctx, store.workerIndexKey(), now)
	if err != nil {
		return nil, err
	}
	var result []WorkerSnapshot
	if err := store.decodeMany(ctx, keys, func(raw []byte) error {
		var snapshot WorkerSnapshot
		if err := json.Unmarshal(raw, &snapshot); err != nil {
			return err
		}
		result = append(result, snapshot)
		return nil
	}); err != nil {
		return nil, err
	}
	return result, nil
}

func (store *RedisStore) ListTasks(ctx context.Context, now time.Time) ([]TaskSnapshot, error) {
	keys, err := store.recentKeys(ctx, store.taskIndexKey(), now)
	if err != nil {
		return nil, err
	}
	var result []TaskSnapshot
	if err := store.decodeMany(ctx, keys, func(raw []byte) error {
		var snapshot TaskSnapshot
		if err := json.Unmarshal(raw, &snapshot); err != nil {
			return err
		}
		result = append(result, snapshot)
		return nil
	}); err != nil {
		return nil, err
	}
	return result, nil
}

func (store *RedisStore) GetSourceAttempt(ctx context.Context, sourceID uint64) (*SourceAttempt, error) {
	raw, err := store.client.Get(ctx, store.sourceKey(sourceID)).Bytes()
	if errors.Is(err, goredis.Nil) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var snapshot SourceAttempt
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		return nil, err
	}
	return &snapshot, nil
}

func (store *RedisStore) saveIndexed(ctx context.Context, index, key string, at time.Time, value any) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if err := store.client.Set(ctx, key, encoded, store.retention).Err(); err != nil {
		return err
	}
	if err := store.client.ZAdd(ctx, index, goredis.Z{Score: float64(at.UnixMilli()), Member: key}).Err(); err != nil {
		return err
	}
	return store.client.Expire(ctx, index, store.retention).Err()
}

func (store *RedisStore) recentKeys(ctx context.Context, index string, now time.Time) ([]string, error) {
	cutoff := now.Add(-store.retention).UnixMilli()
	if err := store.client.ZRemRangeByScore(ctx, index, "-inf", strconv.FormatInt(cutoff-1, 10)).Err(); err != nil {
		return nil, err
	}
	return store.client.ZRangeByScore(ctx, index, &goredis.ZRangeBy{
		Min: strconv.FormatInt(cutoff, 10), Max: "+inf",
	}).Result()
}

func (store *RedisStore) decodeMany(ctx context.Context, keys []string, decode func([]byte) error) error {
	if len(keys) == 0 {
		return nil
	}
	values, err := store.client.MGet(ctx, keys...).Result()
	if err != nil {
		return err
	}
	for index, value := range values {
		if value == nil {
			continue
		}
		var raw []byte
		switch typed := value.(type) {
		case string:
			raw = []byte(typed)
		case []byte:
			raw = typed
		default:
			return fmt.Errorf("decode operational snapshot %s: unexpected Redis value", keys[index])
		}
		if err := decode(raw); err != nil {
			return fmt.Errorf("decode operational snapshot %s: %w", keys[index], err)
		}
	}
	return nil
}

func (store *RedisStore) workerIndexKey() string     { return store.prefix + ":workers" }
func (store *RedisStore) workerKey(id string) string { return store.prefix + ":worker:" + id }
func (store *RedisStore) taskIndexKey() string       { return store.prefix + ":tasks" }
func (store *RedisStore) taskKey(task, instance string) string {
	if instance == "" {
		return store.prefix + ":task:" + task
	}
	return store.prefix + ":task:" + task + ":" + instance
}
func (store *RedisStore) sourceKey(id uint64) string {
	return store.prefix + ":collector:source:" + strconv.FormatUint(id, 10)
}
