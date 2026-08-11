package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"signalwatch/internal/platform/config"
	"signalwatch/internal/platform/logging"
)

const (
	serviceName = "signalwatch-worker"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		slog.Error("load config failed", "service", serviceName, "error", err)
		os.Exit(1)
	}

	logger, err := logging.New(os.Stdout, serviceName, cfg.LogLevel)
	if err != nil {
		slog.Error("initialize logger failed", "service", serviceName, "error", err)
		os.Exit(1)
	}

	// 创建能够监听 os.Interrupt 和 syscall.SIGTERM 的 Context。
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	// 记录 Worker 启动日志。
	logger.Info(
		"worker starting",
		"module", "worker",
		"heartbeat", cfg.WorkerHeartbeat,
		"env", cfg.AppEnv,
	)
	// 调用 run，启动 Worker 的长期运行循环。
	run(ctx, logger, cfg.WorkerHeartbeat)
	// run 返回后，记录 Worker 已停止的日志。
	logger.Info("worker stopped", "module", "worker")
}

func run(ctx context.Context, logger *slog.Logger, interval time.Duration) {
	// 按照 interval 创建 Ticker(定时器)，并确保函数退出时停止它。
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	// 使用循环和 select，同时等待退出信号与心跳事件。
	// 收到退出信号时记录日志并返回。
	// 收到心跳事件时记录心跳日志。
	for {
		select {
		case <-ctx.Done():
			logger.Info("worker stopping", "module", "worker")
			return
		case tickedAt := <-ticker.C:
			logger.Info(
				"worker heartbeat",
				"module", "worker",
				"time_at", tickedAt,
			)
		}
	}
}
