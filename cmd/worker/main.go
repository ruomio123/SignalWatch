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

	"signalwatch/internal/collector"
	"signalwatch/internal/digest"
	"signalwatch/internal/matcher"
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
	limiter, err := arxiv.NewRedisLimiter(
		redisClient,
		"signalwatch:arxiv:next_allowed_at",
		cfg.ArXivRequestInterval,
	)
	if err != nil {
		logger.Error("initialize arxiv limiter failed", "module", "collector", "error", err)
		os.Exit(1)
	}
	lockManager, err := collector.NewRedisLockManager(redisClient, "signalwatch:collector")
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
	digestCoordinator, err := digest.NewRedisCoordinator(
		redisClient, "signalwatch:digest", cfg.DigestLockTTL, cfg.DigestCompletionTTL,
	)
	if err != nil {
		logger.Error("initialize digest coordinator failed", "module", "digest", "error", err)
		os.Exit(1)
	}
	smtpSender, err := digest.NewSMTPSender(digest.SMTPConfig{
		Addr: cfg.SMTPAddr, From: cfg.SMTPFrom, Username: cfg.SMTPUsername,
		Password: cfg.SMTPPassword, StartTLS: cfg.SMTPStartTLS, Timeout: cfg.SMTPTimeout,
	}, func() time.Time { return time.Now().UTC() })
	if err != nil {
		logger.Error("initialize SMTP sender failed", "module", "digest", "error", err)
		os.Exit(1)
	}
	digestProcessor, err := digest.NewProcessor(
		digestRepository, digestCoordinator, smtpSender, func() time.Time { return time.Now().UTC() },
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
	run(ctx, logger, cfg.WorkerHeartbeat, cfg.DigestInterval, digestScheduler.Run)
	<-collectorDone
	<-matcherDone
	<-mailDone
	// run 返回后，记录 Worker 已停止的日志。
	logger.Info("worker stopped", "module", "worker")
}

func run(
	ctx context.Context,
	logger *slog.Logger,
	heartbeatInterval time.Duration,
	digestInterval time.Duration,
	scheduleDigests func(context.Context) (digest.ScheduleResult, error),
) {
	// 按照 interval 创建 Ticker(定时器)，并确保函数退出时停止它。
	heartbeat := time.NewTicker(heartbeatInterval)
	defer heartbeat.Stop()
	digestTicker := time.NewTicker(digestInterval)
	defer digestTicker.Stop()
	runDigestCycle(ctx, logger, scheduleDigests)
	// 使用循环和 select，同时等待退出信号与心跳事件。
	// 收到退出信号时记录日志并返回。
	// 收到心跳事件时记录心跳日志。
	for {
		select {
		case <-ctx.Done():
			logger.Info("worker stopping", "module", "worker")
			return
		case tickedAt := <-heartbeat.C:
			logger.Info(
				"worker heartbeat",
				"module", "worker",
				"time_at", tickedAt,
			)
		case <-digestTicker.C:
			runDigestCycle(ctx, logger, scheduleDigests)
		}
	}
}

func runDigestCycle(
	ctx context.Context,
	logger *slog.Logger,
	schedule func(context.Context) (digest.ScheduleResult, error),
) {
	result, err := schedule(ctx)
	if err != nil {
		if !errors.Is(err, context.Canceled) {
			logger.Error("digest schedule cycle failed", "module", "digest", "error", err)
		}
		return
	}
	logger.Info(
		"digest schedule cycle finished", "module", "digest", "users", result.Users,
		"due", result.Due, "submitted", result.Submitted, "invalid", result.Invalid,
	)
}
