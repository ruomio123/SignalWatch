package main

import (
	"context"
	"log/slog"
	"os"
	"time"

	"signalwatch/internal/auth"
	"signalwatch/internal/paper"
	"signalwatch/internal/platform/config"
	"signalwatch/internal/platform/db"
	"signalwatch/internal/platform/logging"
	"signalwatch/internal/platform/redis"
	"signalwatch/internal/server"
	"signalwatch/internal/source"
	"signalwatch/internal/subscription"
	"signalwatch/internal/user"
)

const serviceName = "signalwatch-api"

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
	// 数据库初始化必须发生在 HTTP 服务启动之前。
	// 否则 API 虽然监听了端口，实际却无法使用数据库。
	database, err := db.Open(cfg)
	if err != nil {
		logger.Error(
			"initialize mysql failed",
			"module", "database",
			"error", err,
		)
		os.Exit(1)
	}
	// 获取底层连接池，进程正常退出时关闭它。
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
	// MySQL 初始化成功后再初始化 Redis。
	redisClient, err := redis.Open(cfg)
	if err != nil {
		logger.Error(
			"initialize redis failed",
			"module", "redis",
			"error", err,
		)
		os.Exit(1)
	}
	defer func() {
		if err := redisClient.Close(); err != nil {
			logger.Error(
				"close redis client failed",
				"module", "redis",
				"error", err,
			)
		}
	}()
	// 将 Redis 的 PING 包装成统一的依赖检查函数。
	// Ping 返回一个命令对象，调用 Err 才能取得本次检查的执行结果。
	redisCheck := func(ctx context.Context) error {
		return redisClient.Ping(ctx).Err()
	}

	// main 是组合根：在这里使用真实数据库组装业务依赖。
	// internal/server 只负责中间件和路由注册。
	userRepository := user.NewRepository(database)
	userService := user.NewService(userRepository)
	userHandler := user.NewHandler(userService, logger)
	tokenService, err := auth.NewTokenService(
		[]byte(cfg.JWTSecret),
		cfg.JWTIssuer,
		cfg.JWTTTL,
		time.Now,
	)
	if err != nil {
		logger.Error(
			"initialize token service failed",
			"module", "auth",
			"error", err,
		)
		os.Exit(1)
	}
	authService := auth.NewService(userRepository, tokenService)
	authHandler := auth.NewHandler(authService, logger)
	sourceRepository := source.NewRepository(database)
	sourceService := source.NewService(sourceRepository)
	sourceHandler := source.NewHandler(sourceService, logger)
	subscriptionRepository := subscription.NewRepository(database)
	subscriptionService := subscription.NewService(subscriptionRepository, sourceService)
	subscriptionHandler := subscription.NewHandler(subscriptionService, logger)
	paperQueryService := paper.NewQueryService(paper.NewQueryRepository(database))
	paperQueryHandler := paper.NewQueryHandler(paperQueryService, logger)

	router, err := server.NewRouter(server.Dependencies{
		AppEnv:                    cfg.AppEnv,
		ServiceName:               serviceName,
		Logger:                    logger,
		MySQLCheck:                sqlDB.PingContext,
		RedisCheck:                redisCheck,
		RegisterHandler:           userHandler.Register,
		LoginHandler:              authHandler.Login,
		AuthMiddleware:            auth.Middleware(tokenService),
		GetProfileHandler:         userHandler.GetProfile,
		UpdateProfileHandler:      userHandler.UpdateProfile,
		ListSourcesHandler:        sourceHandler.List,
		GetSourceHandler:          sourceHandler.Get,
		CreateSubscriptionHandler: subscriptionHandler.Create,
		ListSubscriptionsHandler:  subscriptionHandler.List,
		GetSubscriptionHandler:    subscriptionHandler.Get,
		UpdateSubscriptionHandler: subscriptionHandler.Update,
		DeleteSubscriptionHandler: subscriptionHandler.Delete,
		ListPapersHandler:         paperQueryHandler.List,
		GetPaperHandler:           paperQueryHandler.Get,
	})
	if err != nil {
		logger.Error(
			"initialize http router failed",
			"module", "http",
			"error", err,
		)
		os.Exit(1)
	}

	logger.Info(
		"api server starting",
		"module", "http",
		"address", cfg.HTTPAddr,
		"env", cfg.AppEnv,
	)

	// 当前只需要基础监听能力，直接由 Gin 启动 HTTP 服务。
	err = router.Run(cfg.HTTPAddr)
	if err != nil {
		logger.Error("api server stopped", "module", "http", "error", err)
		return
	}
}
