package main

import (
	"context"
	"log/slog"
	"os"

	"signalwatch/internal/platform/config"
	"signalwatch/internal/platform/db"
	"signalwatch/internal/platform/logging"
	"signalwatch/internal/platform/redis"
	"signalwatch/internal/server"
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

	// 入口层只负责提供真实依赖；路由、中间件和 Handler
	// 由 internal/server 统一组装。
	router, err := server.NewRouter(server.Dependencies{
		AppEnv:      cfg.AppEnv,
		ServiceName: serviceName,
		Logger:      logger,
		MySQLCheck:  sqlDB.PingContext,
		RedisCheck:  redisCheck,
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
