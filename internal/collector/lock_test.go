package collector

import (
	"context"
	"testing"
	"time"

	goredis "github.com/redis/go-redis/v9"
)

type redisCommandsStub struct {
	setNXResult bool
	setNXKey    string
	setNXValue  any
	setNXTTL    time.Duration
	evalScript  string
	evalKeys    []string
	evalArgs    []any
}

func (stub *redisCommandsStub) SetNX(
	ctx context.Context,
	key string,
	value any,
	expiration time.Duration,
) *goredis.BoolCmd {
	stub.setNXKey = key
	stub.setNXValue = value
	stub.setNXTTL = expiration
	command := goredis.NewBoolCmd(ctx)
	command.SetVal(stub.setNXResult)
	return command
}

func (stub *redisCommandsStub) Eval(
	ctx context.Context,
	script string,
	keys []string,
	args ...any,
) *goredis.Cmd {
	stub.evalScript = script
	stub.evalKeys = append([]string(nil), keys...)
	stub.evalArgs = append([]any(nil), args...)
	command := goredis.NewCmd(ctx)
	command.SetVal(int64(1))
	return command
}

func TestRedisLockManagerSkipsWhenAnotherWorkerOwnsLock(t *testing.T) {
	commands := &redisCommandsStub{setNXResult: false}
	manager, err := NewRedisLockManager(commands, "signalwatch:collector")
	if err != nil {
		t.Fatalf("new lock manager: %v", err)
	}
	release, acquired, err := manager.Acquire(context.Background(), time.Minute)
	if err != nil || acquired || release != nil {
		t.Fatalf("expected lock contention, acquired=%v release=%v err=%v", acquired, release != nil, err)
	}
}

func TestRedisLockManagerReleasesOnlyItsOwnershipToken(t *testing.T) {
	commands := &redisCommandsStub{setNXResult: true}
	manager, err := NewRedisLockManager(commands, "signalwatch:collector")
	if err != nil {
		t.Fatalf("new lock manager: %v", err)
	}
	release, acquired, err := manager.Acquire(context.Background(), 55*time.Minute)
	if err != nil || !acquired || release == nil {
		t.Fatalf("acquire lock: acquired=%v err=%v", acquired, err)
	}
	if commands.setNXKey != "signalwatch:collector" || commands.setNXTTL != 55*time.Minute || commands.setNXValue == "" {
		t.Fatalf("unexpected lock reservation: key=%q ttl=%v token=%v", commands.setNXKey, commands.setNXTTL, commands.setNXValue)
	}
	if err := release(context.Background()); err != nil {
		t.Fatalf("release lock: %v", err)
	}
	if commands.evalScript != releaseLockScript || len(commands.evalKeys) != 1 ||
		commands.evalKeys[0] != "signalwatch:collector" || len(commands.evalArgs) != 1 ||
		commands.evalArgs[0] != commands.setNXValue {
		t.Fatalf("release must compare the acquired ownership token")
	}
}
