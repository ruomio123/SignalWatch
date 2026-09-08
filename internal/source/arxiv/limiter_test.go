package arxiv

import (
	"context"
	"errors"
	"testing"
	"time"

	goredis "github.com/redis/go-redis/v9"
)

type scriptRunnerStub struct {
	script string
	keys   []string
	args   []any
	result int64
	err    error
}

func (stub *scriptRunnerStub) Eval(
	ctx context.Context,
	script string,
	keys []string,
	args ...any,
) *goredis.Cmd {
	stub.script = script
	stub.keys = append([]string(nil), keys...)
	stub.args = append([]any(nil), args...)
	command := goredis.NewCmd(ctx)
	command.SetVal(stub.result)
	command.SetErr(stub.err)
	return command
}

func TestRedisLimiterRequiresAtLeastThreeSeconds(t *testing.T) {
	if _, err := NewRedisLimiter(&scriptRunnerStub{}, "arxiv", 3*time.Second-time.Millisecond); err == nil {
		t.Fatal("expected intervals below three seconds to be rejected")
	}
	if _, err := NewRedisLimiter(&scriptRunnerStub{}, "arxiv", 3*time.Second); err != nil {
		t.Fatalf("three-second interval must be accepted: %v", err)
	}
}

func TestRedisLimiterReservesGlobalRedisTimeSlot(t *testing.T) {
	runner := &scriptRunnerStub{}
	limiter, err := NewRedisLimiter(runner, "signalwatch:arxiv", 3*time.Second)
	if err != nil {
		t.Fatalf("new limiter: %v", err)
	}
	if err := limiter.Wait(context.Background()); err != nil {
		t.Fatalf("wait: %v", err)
	}
	if runner.script != limiterScript || len(runner.keys) != 1 || runner.keys[0] != "signalwatch:arxiv" {
		t.Fatalf("unexpected limiter script invocation: keys=%v", runner.keys)
	}
	if len(runner.args) != 2 || runner.args[0] != int64(3000) || runner.args[1] != int64(30000) {
		t.Fatalf("unexpected interval and ttl arguments: %v", runner.args)
	}
}

func TestRedisLimiterWaitHonorsCancellation(t *testing.T) {
	runner := &scriptRunnerStub{result: 1000}
	limiter, err := NewRedisLimiter(runner, "signalwatch:arxiv", 3*time.Second)
	if err != nil {
		t.Fatalf("new limiter: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := limiter.Wait(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected canceled wait, got %v", err)
	}
}
