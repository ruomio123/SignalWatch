package main

import (
	"log/slog"
	"net/http"
	"os"

	"signalwatch/internal/platform/config"
	"signalwatch/internal/platform/db"
	"signalwatch/internal/platform/logging"
	"signalwatch/internal/platform/redis"
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
	mux := http.NewServeMux() //创建独立的 ServeMux

	//注册带请求方法的路由
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {

		//构造 JSON 响应
		w.Header().Set("Content-Type", "application/json")                       //1.设置响应头
		w.WriteHeader(200)                                                       //2.设置状态码
		_, err := w.Write([]byte(`{"status":"ok","service":"signalwatch-api"}`)) //3.写入响应正文
		if err != nil {
			logger.Error("write health response failed", "module", "http", "error", err)
			return
		}
	})

	//创建自己的 HTTP Server
	server := &http.Server{
		Addr:    cfg.HTTPAddr,
		Handler: mux,
	}
	//启动服务；出现错误时记录日志并退出

	logger.Info(
		"api server starting",
		"module", "http",
		"address", cfg.HTTPAddr,
		"env", cfg.AppEnv,
	)
	err = server.ListenAndServe()
	if err != nil {
		logger.Error("api server stopped", "module", "http", "error", err)
		return
	}
}
