package operations

import (
	"context"
	"sort"
	"strconv"
	"testing"
	"time"

	goredis "github.com/redis/go-redis/v9"
)

type memoryRedis struct {
	values  map[string]string
	zsets   map[string]map[string]float64
	expires map[string]time.Duration
}

func newMemoryRedis() *memoryRedis {
	return &memoryRedis{values: map[string]string{}, zsets: map[string]map[string]float64{}, expires: map[string]time.Duration{}}
}

func (redis *memoryRedis) Set(ctx context.Context, key string, value any, expiration time.Duration) *goredis.StatusCmd {
	command := goredis.NewStatusCmd(ctx)
	switch typed := value.(type) {
	case []byte:
		redis.values[key] = string(typed)
	case string:
		redis.values[key] = typed
	}
	redis.expires[key] = expiration
	command.SetVal("OK")
	return command
}

func (redis *memoryRedis) Get(ctx context.Context, key string) *goredis.StringCmd {
	command := goredis.NewStringCmd(ctx)
	value, ok := redis.values[key]
	if !ok {
		command.SetErr(goredis.Nil)
		return command
	}
	command.SetVal(value)
	return command
}

func (redis *memoryRedis) MGet(ctx context.Context, keys ...string) *goredis.SliceCmd {
	command := goredis.NewSliceCmd(ctx)
	values := make([]any, len(keys))
	for index, key := range keys {
		if value, ok := redis.values[key]; ok {
			values[index] = value
		}
	}
	command.SetVal(values)
	return command
}

func (redis *memoryRedis) ZAdd(ctx context.Context, key string, members ...goredis.Z) *goredis.IntCmd {
	command := goredis.NewIntCmd(ctx)
	if redis.zsets[key] == nil {
		redis.zsets[key] = map[string]float64{}
	}
	for _, member := range members {
		redis.zsets[key][member.Member.(string)] = member.Score
	}
	command.SetVal(int64(len(members)))
	return command
}

func (redis *memoryRedis) ZRangeByScore(ctx context.Context, key string, rangeBy *goredis.ZRangeBy) *goredis.StringSliceCmd {
	command := goredis.NewStringSliceCmd(ctx)
	min, _ := strconv.ParseFloat(rangeBy.Min, 64)
	max := 1.7976931348623157e+308
	if rangeBy.Max != "+inf" {
		max, _ = strconv.ParseFloat(rangeBy.Max, 64)
	}
	values := make([]string, 0)
	for member, score := range redis.zsets[key] {
		if score >= min && score <= max {
			values = append(values, member)
		}
	}
	sort.Strings(values)
	command.SetVal(values)
	return command
}

func (redis *memoryRedis) ZRemRangeByScore(ctx context.Context, key, min, max string) *goredis.IntCmd {
	command := goredis.NewIntCmd(ctx)
	upper, _ := strconv.ParseFloat(max, 64)
	var removed int64
	for member, score := range redis.zsets[key] {
		if score <= upper {
			delete(redis.zsets[key], member)
			removed++
		}
	}
	command.SetVal(removed)
	return command
}

func (redis *memoryRedis) Expire(ctx context.Context, key string, expiration time.Duration) *goredis.BoolCmd {
	command := goredis.NewBoolCmd(ctx)
	redis.expires[key] = expiration
	command.SetVal(true)
	return command
}

func TestRedisStoreRetainsMultipleWorkersAndCleansStaleIndexMembers(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, time.September, 9, 8, 0, 0, 0, time.UTC)
	client := newMemoryRedis()
	store, err := NewRedisStore(client, "test:ops", 2*time.Hour)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	if err := store.SaveWorker(ctx, WorkerSnapshot{InstanceID: "recent", State: "running", LastHeartbeatAt: now}); err != nil {
		t.Fatalf("save recent worker: %v", err)
	}
	if err := store.SaveWorker(ctx, WorkerSnapshot{InstanceID: "old", State: "stopped", LastHeartbeatAt: now.Add(-3 * time.Hour)}); err != nil {
		t.Fatalf("save old worker: %v", err)
	}
	workers, err := store.ListWorkers(ctx, now)
	if err != nil {
		t.Fatalf("list workers: %v", err)
	}
	if len(workers) != 1 || workers[0].InstanceID != "recent" {
		t.Fatalf("expected only recent worker, got %+v", workers)
	}
	if client.expires[store.workerIndexKey()] != 2*time.Hour || client.expires[store.workerKey("recent")] != 2*time.Hour {
		t.Fatalf("expected both index and value retention, got %+v", client.expires)
	}
}

func TestRedisStoreRoundTripsTaskAndSourceSnapshots(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, time.September, 9, 8, 0, 0, 0, time.UTC)
	client := newMemoryRedis()
	store, err := NewRedisStore(client, "test:ops", time.Hour)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	wantTask := TaskSnapshot{Task: "digest", InstanceID: "worker-a", State: "succeeded", UpdatedAt: now}
	if err := store.SaveTask(ctx, wantTask); err != nil {
		t.Fatalf("save task: %v", err)
	}
	tasks, err := store.ListTasks(ctx, now)
	if err != nil || len(tasks) != 1 || tasks[0].Task != wantTask.Task || tasks[0].InstanceID != wantTask.InstanceID {
		t.Fatalf("round-trip task: tasks=%+v error=%v", tasks, err)
	}
	wantSource := SourceAttempt{SourceID: 9, State: "failed", Mode: "daily", AttemptID: "attempt", StartedAt: now}
	if err := store.SaveSourceAttempt(ctx, wantSource); err != nil {
		t.Fatalf("save source attempt: %v", err)
	}
	gotSource, err := store.GetSourceAttempt(ctx, wantSource.SourceID)
	if err != nil || gotSource == nil || gotSource.AttemptID != wantSource.AttemptID {
		t.Fatalf("round-trip source: source=%+v error=%v", gotSource, err)
	}
	missing, err := store.GetSourceAttempt(ctx, 404)
	if err != nil || missing != nil {
		t.Fatalf("missing source status should be nil: source=%+v error=%v", missing, err)
	}
}
