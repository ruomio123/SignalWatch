package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"signalwatch/internal/backfill"
	"signalwatch/internal/bootstrap"
	"signalwatch/internal/collector"
	"signalwatch/internal/digest"
	"signalwatch/internal/matcher"
	"signalwatch/internal/operations"
	"signalwatch/internal/paper"
	"signalwatch/internal/platform/config"
	"signalwatch/internal/platform/db"
	"signalwatch/internal/platform/logging"
	"signalwatch/internal/platform/redis"
	"signalwatch/internal/source/arxiv"
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
	// Worker 会读写论文、匹配和投递记录，因此启动前必须确认 MySQL 可用。
	database, err := db.Open(cfg)
	if err != nil {
		logger.Error(
			"initialize mysql failed",
			"module", "database",
			"error", err,
		)
		os.Exit(1)
	}
	sqlDB, err := database.DB()
	if err != nil {
		logger.Error(
			"get mysql connection pool failed",
			"module", "database",
			"error", err,
		)
		os.Exit(1)
	}
	defer sqlDB.Close()
	// Worker 开始心跳循环之前，先验证 Redis 是否可用。
	redisClient, err := redis.Open(cfg)
	if err != nil {
		logger.Error(
			"initialize redis failed",
			"module", "redis",
			"error", err,
		)
		os.Exit(1)
	}
	// Worker 收到退出信号、run 返回、main 正常结束时，
	// defer 会关闭 Redis Client。
	defer func() {
		if err := redisClient.Close(); err != nil {
			logger.Error(
				"close redis client failed",
				"module", "redis",
				"error", err,
			)
		}
	}()
	// 创建能够监听 os.Interrupt 和 syscall.SIGTERM 的 Context。
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	opsStore, err := operations.NewRedisStore(redisClient, "signalwatch:ops", cfg.OpsStatusRetention)
	if err != nil {
		logger.Error("initialize operations store failed", "module", "operations", "error", err)
		os.Exit(1)
	}
	opsReporter, err := operations.NewReporter(
		opsStore, logger, func() time.Time { return time.Now().UTC() }, cfg.WorkerHeartbeat,
	)
	if err != nil {
		logger.Error("initialize operations reporter failed", "module", "operations", "error", err)
		os.Exit(1)
	}
	logger = logger.With("worker_instance_id", opsReporter.InstanceID())
	limiter, err := arxiv.NewRedisLimiter(
		redisClient,
		"signalwatch:arxiv:next_allowed_at",
		cfg.ArXivRequestInterval,
	)
	if err != nil {
		logger.Error("initialize arxiv limiter failed", "module", "collector", "error", err)
		os.Exit(1)
	}
	lockManager, err := collector.NewMySQLLockManager(database, "signalwatch:collector")
	if err != nil {
		logger.Error("initialize collector lock failed", "module", "collector", "error", err)
		os.Exit(1)
	}
	matcherService, err := matcher.NewService(
		matcher.NewRepository(database),
		func() time.Time { return time.Now().UTC() },
	)
	if err != nil {
		logger.Error("initialize matcher service failed", "module", "matcher", "error", err)
		os.Exit(1)
	}
	matcherPool, err := matcher.NewPool(matcherService, logger, matcher.PoolConfig{
		Workers: cfg.MatcherWorkers, QueueCapacity: cfg.MatcherQueueCapacity,
	})
	if err != nil {
		logger.Error("initialize matcher pool failed", "module", "matcher", "error", err)
		os.Exit(1)
	}
	arXivTransport := http.DefaultTransport.(*http.Transport).Clone()
	arXivTransport.MaxConnsPerHost = 1
	arXivTransport.MaxIdleConnsPerHost = 1
	collectorService, err := collector.NewService(
		collector.NewRepository(database),
		paper.NewRepository(database),
		matcherPool,
		lockManager,
		collector.ArXivClientFactory{
			HTTPClient: &http.Client{Timeout: cfg.ArXivHTTPTimeout, Transport: arXivTransport},
			Limiter:    limiter,
			SearchConfig: arxiv.Config{
				PageSize:         cfg.ArXivPageSize,
				MaxResponseBytes: cfg.ArXivMaxResponseBytes,
				RequestAttempts:  cfg.ArXivRequestAttempts,
				RetryBackoff:     cfg.ArXivRequestBackoff,
			},
			FeedConfig: arxiv.FeedConfig{
				Endpoint: cfg.ArXivFeedEndpoint, MaxResponseBytes: cfg.ArXivMaxResponseBytes,
				RequestAttempts: cfg.ArXivRequestAttempts, RetryBackoff: cfg.ArXivRequestBackoff,
			},
		},
		logger,
		func() time.Time { return time.Now().UTC() },
		collector.Config{
			BootstrapLookback: cfg.ArXivBootstrapLookback,
			RecoveryOverlap:   cfg.ArXivRecoveryOverlap,
			LockTTL:           cfg.CollectorLockTTL, PageSize: cfg.ArXivPageSize,
			MaxPages: cfg.ArXivMaxPages,
		},
	)
	if err != nil {
		logger.Error("initialize collector service failed", "module", "collector", "error", err)
		os.Exit(1)
	}
	collectorScheduler, err := collector.NewScheduler(
		collectorService, logger, func() time.Time { return time.Now().UTC() },
		collector.SchedulerConfig{
			DailySyncTime: cfg.ArXivDailySyncTime, RetryInterval: cfg.ArXivSyncRetryInterval,
		},
	)
	if err != nil {
		logger.Error("initialize collector scheduler failed", "module", "collector", "error", err)
		os.Exit(1)
	}
	digestRepository := digest.NewRepository(database)
	digestCoordinator := digest.NewMySQLDeliveryStore(database)
	smtpSender, err := digest.NewSMTPSender(digest.SMTPConfig{
		Addr: cfg.SMTPAddr, From: cfg.SMTPFrom, Username: cfg.SMTPUsername,
		Password: cfg.SMTPPassword, StartTLS: cfg.SMTPStartTLS, Timeout: cfg.SMTPTimeout,
	}, func() time.Time { return time.Now().UTC() })
	if err != nil {
		logger.Error("initialize SMTP sender failed", "module", "digest", "error", err)
		os.Exit(1)
	}
	aiService, closeAI, err := bootstrap.OpenAI(cfg, logger, digestCoordinator)
	if err != nil {
		logger.Error("initialize AI failed", "module", "ai")
		os.Exit(1)
	}
	defer closeAI()
	digestProcessor, err := digest.NewProcessor(
		digestRepository, digestCoordinator, smtpSender, func() time.Time { return time.Now().UTC() }, digest.Options{PublicBaseURL: cfg.PublicBaseURL, Enricher: aiService},
	)
	if err != nil {
		logger.Error("initialize digest processor failed", "module", "digest", "error", err)
		os.Exit(1)
	}
	mailPool, err := digest.NewPool(digestProcessor, logger, digest.PoolConfig{
		Workers: cfg.MailWorkers, QueueCapacity: cfg.MailQueueCapacity,
	})
	if err != nil {
		logger.Error("initialize mail pool failed", "module", "digest", "error", err)
		os.Exit(1)
	}
	collectorService.SetObserver(opsReporter)
	collectorScheduler.SetObserver(opsReporter)
	digestScheduler, err := digest.NewScheduler(
		digestRepository, mailPool, logger, func() time.Time { return time.Now().UTC() },
	)
	if err != nil {
		logger.Error("initialize digest scheduler failed", "module", "digest", "error", err)
		os.Exit(1)
	}
	// 记录 Worker 启动日志。
	logger.Info(
		"worker starting",
		"module", "worker",
		"event", "worker_starting",
		"worker_instance_id", opsReporter.InstanceID(),
		"heartbeat", cfg.WorkerHeartbeat,
		"arxiv_daily_sync_time", cfg.ArXivDailySyncTime,
		"arxiv_daily_sync_timezone", "America/New_York",
		"arxiv_sync_retry_interval", cfg.ArXivSyncRetryInterval,
		"matcher_workers", cfg.MatcherWorkers,
		"matcher_queue_capacity", cfg.MatcherQueueCapacity,
		"digest_interval", cfg.DigestInterval,
		"mail_workers", cfg.MailWorkers,
		"mail_queue_capacity", cfg.MailQueueCapacity,
		"env", cfg.AppEnv,
	)
	backfillDone := make(chan struct{})
	go func() { defer close(backfillDone); backfill.New(backfill.NewMySQLStore(database), logger).Run(ctx) }()
	aiDone := make(chan struct{})
	go func() { defer close(aiDone); aiService.Run(ctx) }()
	matcherDone := make(chan struct{})
	go func() {
		defer close(matcherDone)
		matcherPool.Run(ctx)
	}()
	collectorDone := make(chan struct{})
	go func() {
		defer close(collectorDone)
		collectorScheduler.Run(ctx)
	}()
	mailDone := make(chan struct{})
	go func() {
		defer close(mailDone)
		mailPool.Run(ctx)
	}()
	// 调用 run，启动 Worker 的长期运行循环。
	reportHeartbeat := func(reportContext context.Context) {
		matcherStats, mailStats := matcherPool.Stats(), mailPool.Stats()
		matcherQueue, mailQueue := matcherQueueSnapshot(matcherStats), mailQueueSnapshot(mailStats)
		opsReporter.Heartbeat(reportContext, matcherQueue, mailQueue)
		updatedAt := time.Now().UTC()
		if cfg.AIEnabled {
			report, cancel := context.WithTimeout(reportContext, 200*time.Millisecond)
			values, aiErr := aiService.Stats(report)
			cancel()
			state := "running"
			if aiErr != nil {
				state = "failed"
			}
			metrics := map[string]int{}
			for key, value := range values {
				switch number := value.(type) {
				case int:
					metrics[key] = number
				case int64:
					metrics[key] = int(number)
				}
			}
			opsReporter.RecordTask(reportContext, operations.TaskSnapshot{Task: "ai", State: state, UpdatedAt: updatedAt, Metrics: metrics})
		}
		opsReporter.RecordTask(reportContext, queueTaskSnapshot("matcher", updatedAt, matcherQueue, matcherStats.LastSuccessAt, matcherStats.LastFailureAt))
		opsReporter.RecordTask(reportContext, queueTaskSnapshot("mail", updatedAt, mailQueue, mailStats.LastSuccessAt, mailStats.LastFailureAt))
	}
	scheduleDigests := func(scheduleContext context.Context) (digest.ScheduleResult, error) {
		startedAt := time.Now().UTC()
		result, scheduleErr := digestScheduler.Run(scheduleContext)
		updatedAt := time.Now().UTC()
		state := "succeeded"
		task := operations.TaskSnapshot{
			Task: "digest", State: state, StartedAt: &startedAt, UpdatedAt: updatedAt,
			Metrics: map[string]int{"users": result.Users, "due": result.Due, "submitted": result.Submitted, "invalid": result.Invalid},
		}
		if scheduleErr != nil {
			task.State = "failed"
			task.Failure = operations.SafeFailure("schedule", updatedAt)
		} else {
			task.LastSuccessAt = &updatedAt
		}
		opsReporter.RecordTask(scheduleContext, task)
		return result, scheduleErr
	}
	run(ctx, logger, cfg.WorkerHeartbeat, cfg.DigestInterval, reportHeartbeat, scheduleDigests)
	<-backfillDone
	<-aiDone
	<-collectorDone
	<-matcherDone
	<-mailDone
	stopContext, stopCancel := context.WithTimeout(context.Background(), 3*time.Second)
	opsReporter.Stop(stopContext, matcherQueueSnapshot(matcherPool.Stats()), mailQueueSnapshot(mailPool.Stats()))
	stopCancel()
	// run 返回后，记录 Worker 已停止的日志。
	logger.Info("worker stopped", "module", "worker", "event", "worker_stopped", "state", "stopped")
}

func run(
	ctx context.Context,
	logger *slog.Logger,
	heartbeatInterval time.Duration,
	digestInterval time.Duration,
	reportHeartbeat func(context.Context),
	scheduleDigests func(context.Context) (digest.ScheduleResult, error),
) {
	// 按照 interval 创建 Ticker(定时器)，并确保函数退出时停止它。
	heartbeat := time.NewTicker(heartbeatInterval)
	defer heartbeat.Stop()
	reportHeartbeat(ctx)
	// Keep one sequential scheduler independent of heartbeat reporting: queue
	// backpressure must not make a healthy Worker appear offline.
	digestDone := make(chan struct{})
	go func() {
		defer close(digestDone)
		ticker := time.NewTicker(digestInterval)
		defer ticker.Stop()
		runDigestCycle(ctx, logger, scheduleDigests)
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				runDigestCycle(ctx, logger, scheduleDigests)
			}
		}
	}()
	defer func() { <-digestDone }()
	// 使用循环和 select，同时等待退出信号与心跳事件。
	// 收到退出信号时记录日志并返回。
	// 收到心跳事件时记录心跳日志。
	for {
		select {
		case <-ctx.Done():
			logger.Info("worker stopping", "module", "worker", "event", "worker_stopping", "state", "stopping")
			return
		case tickedAt := <-heartbeat.C:
			reportHeartbeat(ctx)
			logger.Info(
				"worker heartbeat",
				"module", "worker",
				"event", "worker_heartbeat",
				"state", "running",
				"time_at", tickedAt,
			)
		}
	}
}

func matcherQueueSnapshot(stats matcher.PoolStats) operations.QueueSnapshot {
	return operations.QueueSnapshot{
		Depth: stats.Depth, Capacity: stats.Capacity, Workers: stats.Workers,
		Processing: stats.Processing, Succeeded: stats.Succeeded, Failed: stats.Failed,
	}
}

func mailQueueSnapshot(stats digest.PoolStats) operations.QueueSnapshot {
	return operations.QueueSnapshot{
		Skipped: stats.Skipped, Locked: stats.Locked, Retried: stats.Retried, Depth: stats.Depth, Capacity: stats.Capacity, Workers: stats.Workers,
		Processing: stats.Processing, Succeeded: stats.Succeeded, Failed: stats.Failed,
	}
}

func queueTaskSnapshot(task string, updatedAt time.Time, queue operations.QueueSnapshot, lastSuccess, lastFailure time.Time) operations.TaskSnapshot {
	state := "idle"
	if queue.Processing > 0 {
		state = "running"
	} else if !lastFailure.IsZero() && (lastSuccess.IsZero() || lastFailure.After(lastSuccess)) {
		state = "failed"
	} else if !lastSuccess.IsZero() {
		state = "succeeded"
	}
	snapshot := operations.TaskSnapshot{
		Task: task, State: state, UpdatedAt: updatedAt,
		Metrics: map[string]int{
			"queue_depth": queue.Depth, "queue_capacity": queue.Capacity,
			"workers": queue.Workers, "processing": int(queue.Processing),
			"succeeded": int(queue.Succeeded), "failed": int(queue.Failed),
		},
	}
	if !lastSuccess.IsZero() {
		value := lastSuccess.UTC()
		snapshot.LastSuccessAt = &value
	}
	if state == "failed" {
		snapshot.Failure = operations.SafeFailure("process", lastFailure)
	}
	return snapshot
}

func runDigestCycle(
	ctx context.Context,
	logger *slog.Logger,
	schedule func(context.Context) (digest.ScheduleResult, error),
) {
	startedAt := time.Now()
	result, err := schedule(ctx)
	if err != nil {
		if !errors.Is(err, context.Canceled) {
			logger.Error("digest schedule cycle failed", "module", "digest", "event", "digest_schedule",
				"task", "digest", "state", "failed", "error_stage", "schedule",
				"duration_ms", time.Since(startedAt).Milliseconds(), "error", err)
		}
		return
	}
	logger.Info(
		"digest schedule cycle finished", "module", "digest", "event", "digest_schedule",
		"task", "digest", "state", "succeeded", "duration_ms", time.Since(startedAt).Milliseconds(),
		"users", result.Users, "subscriptions", result.Subscriptions,
		"due", result.Due, "submitted", result.Submitted, "invalid", result.Invalid,
	)
}
