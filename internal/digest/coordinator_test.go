package digest

import (
	"context"
	"testing"
	"time"

	goredis "github.com/redis/go-redis/v9"
)

type redisStub struct {
	setNXResult bool
	exists      int64
	setNXKey    string
	setNXTTL    time.Duration
	setKey      string
	setTTL      time.Duration
	evalKey     string
	setCalls    int
}

func (stub *redisStub) SetNX(
	ctx context.Context, key string, _ any, expiration time.Duration,
) *goredis.BoolCmd {
	stub.setNXKey, stub.setNXTTL = key, expiration
	command := goredis.NewBoolCmd(ctx)
	command.SetVal(stub.setNXResult)
	return command
}
func (stub *redisStub) Exists(ctx context.Context, _ ...string) *goredis.IntCmd {
	command := goredis.NewIntCmd(ctx)
	command.SetVal(stub.exists)
	return command
}
func (stub *redisStub) Set(
	ctx context.Context, key string, _ any, expiration time.Duration,
) *goredis.StatusCmd {
	stub.setKey, stub.setTTL = key, expiration
	stub.setCalls++
	command := goredis.NewStatusCmd(ctx)
	command.SetVal("OK")
	return command
}
func (stub *redisStub) Eval(
	ctx context.Context, _ string, keys []string, _ ...any,
) *goredis.Cmd {
	stub.evalKey = keys[0]
	command := goredis.NewCmd(ctx)
	command.SetVal(int64(1))
	return command
}

func TestRedisCoordinatorSeparatesProcessingLockFromCompletionMarker(t *testing.T) {
	commands := &redisStub{setNXResult: true}
	coordinator, err := NewRedisCoordinator(commands, "signalwatch:digest", 10*time.Minute, 72*time.Hour)
	if err != nil {
		t.Fatalf("new coordinator: %v", err)
	}
	job := Job{UserID: 42, LocalDate: "2026-09-08"}
	release, acquired, err := coordinator.Acquire(t.Context(), job)
	if err != nil || !acquired {
		t.Fatalf("acquire: acquired=%v err=%v", acquired, err)
	}
	if commands.setNXKey != "signalwatch:digest:lock:42:2026-09-08" || commands.setNXTTL != 10*time.Minute {
		t.Fatalf("unexpected lock reservation: key=%s ttl=%s", commands.setNXKey, commands.setNXTTL)
	}
	if commands.setCalls != 0 {
		t.Fatal("acquiring the processing lock must not mark the day complete")
	}
	if err := coordinator.MarkComplete(t.Context(), job); err != nil {
		t.Fatalf("mark complete: %v", err)
	}
	if commands.setKey != "signalwatch:digest:done:42:2026-09-08" || commands.setTTL != 72*time.Hour {
		t.Fatalf("unexpected completion marker: key=%s ttl=%s", commands.setKey, commands.setTTL)
	}
	if err := release(t.Context()); err != nil || commands.evalKey != commands.setNXKey {
		t.Fatalf("release did not target owned lock: eval=%s err=%v", commands.evalKey, err)
	}
}
