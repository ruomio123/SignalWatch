package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"signalwatch/internal/agent"
	"signalwatch/internal/ai"
	"signalwatch/internal/auth"
	"signalwatch/internal/bootstrap"
	"signalwatch/internal/operations"
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
	opsStore, err := operations.NewRedisStore(redisClient, "signalwatch:ops", cfg.OpsStatusRetention)
	if err != nil {
		logger.Error("initialize operations store failed", "module", "operations", "error", err)
		os.Exit(1)
	}

	// main 是组合根：在这里使用真实数据库组装业务依赖。
	// internal/server 只负责中间件和路由注册。
	aiService, closeAI, err := bootstrap.OpenAI(cfg, logger, nil)
	if err != nil {
		logger.Error("initialize AI failed", "module", "ai")
		os.Exit(1)
	}
	defer closeAI()
	var checker user.AIConfigurationChecker
	if aiService.Configurations() != nil {
		checker = aiService.Configurations()
	}
	userRepository := user.NewRepository(database)
	userService := user.NewService(userRepository, checker)
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
	sessionService, err := auth.NewSessionService(auth.NewSessionStore(database), tokenService, cfg.AuthSessionTTL, time.Now)
	if err != nil {
		logger.Error("initialize sessions failed", "module", "auth", "error", err)
		os.Exit(1)
	}
	authService := auth.NewService(userRepository, sessionService)
	authHandler := auth.NewHandler(authService, sessionService, cfg.AppEnv == "production", logger)
	sourceRepository := source.NewRepository(database)
	sourceService := source.NewService(sourceRepository)
	sourceHandler := source.NewHandler(sourceService, logger)
	subscriptionRepository := subscription.NewRepository(database)
	subscriptionService := subscription.NewService(subscriptionRepository, sourceService, checker)
	subscriptionHandler := subscription.NewHandler(subscriptionService, logger)
	paperQueryService := paper.NewQueryService(paper.NewQueryRepository(database))
	paperQueryHandler := paper.NewQueryHandler(paperQueryService, logger)
	opsService, err := operations.NewService(
		database, opsStore, sqlDB.PingContext, redisCheck,
		func() time.Time { return time.Now().UTC() }, cfg.ArXivDailySyncTime,
	)
	if err != nil {
		logger.Error("initialize operations service failed", "module", "operations", "error", err)
		os.Exit(1)
	}
	opsHandler, err := operations.NewHandler(opsService, logger)
	if err != nil {
		logger.Error("initialize operations handler failed", "module", "operations", "error", err)
		os.Exit(1)
	}

	agentService, _ := bootstrap.OpenAgent(database, aiService, nil)
	agentHandler := agent.Handler{Service: agentService}
	aiHandler := ai.Handler{Service: aiService, Configurations: aiService.Configurations(), Calls: aiService.Calls(), Enabled: cfg.AIEnabled}
	router, err := server.NewRouter(server.Dependencies{

		AgentHandler: agentHandler.Handle,
		AppEnv:       cfg.AppEnv,
		ServiceName:  serviceName,
		Logger:       logger,
		MySQLCheck:   sqlDB.PingContext,
		RedisCheck:   redisCheck, AI: server.AIRoutes{GetAISummaryHandler: aiHandler.Get, RequestAISummaryHandler: aiHandler.Request,
			ListAIProvidersHandler: aiHandler.Providers,
			CredentialsHandler:     aiHandler.Credentials, CredentialHandler: aiHandler.Credential, DefaultSelectionHandler: aiHandler.DefaultSelection,
			GetAIConfigurationHandler:    aiHandler.GetConfiguration,
			PutAIConfigurationHandler:    aiHandler.PutConfiguration,
			PatchAIConfigurationHandler:  aiHandler.PatchConfiguration,
			RotateAISecretHandler:        aiHandler.RotateSecret,
			TestAIConfigurationHandler:   aiHandler.TestConfiguration,
			DeleteAIConfigurationHandler: aiHandler.DeleteConfiguration,
			GetAIUsageHandler:            aiHandler.Usage, ListAICallsHandler: aiHandler.ListCalls}, Accounts: server.AccountsRoutes{RegisterHandler: userHandler.Register,
			LoginHandler:   authHandler.Login,
			RefreshHandler: authHandler.Refresh, LogoutHandler: authHandler.Logout,

			GetProfileHandler:    userHandler.GetProfile,
			UpdateProfileHandler: userHandler.UpdateProfile}, Authorization: server.AuthorizationRoutes{AuthMiddleware: auth.Middleware(tokenService),
			ActiveRoleMiddleware:      auth.RequireRoles(userRepository, logger, user.RoleUser, user.RoleOperator),
			UserRoleMiddleware:        auth.RequireRoles(userRepository, logger, user.RoleUser),
			OperatorRoleMiddleware:    auth.RequireRoles(userRepository, logger, user.RoleOperator),
			OperationsAuditMiddleware: operations.AuditMiddleware(logger)}, Sources: server.SourcesRoutes{ListSourcesHandler: sourceHandler.List,
			GetSourceHandler: sourceHandler.Get}, Subscriptions: server.SubscriptionsRoutes{CreateSubscriptionHandler: subscriptionHandler.Create,
			ListSubscriptionsHandler:  subscriptionHandler.List,
			GetSubscriptionHandler:    subscriptionHandler.Get,
			UpdateSubscriptionHandler: subscriptionHandler.Update,
			DeleteSubscriptionHandler: subscriptionHandler.Delete}, Papers: server.PapersRoutes{ListPapersHandler: paperQueryHandler.List,
			GetPaperHandler: paperQueryHandler.Get}, Operations: server.OperationsRoutes{OperationsStatusHandler: opsHandler.Status,
			OperationsSourcesHandler: opsHandler.Sources},
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

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := server.Serve(ctx, cfg.HTTPAddr, router); err != nil {
		logger.Error("api server stopped", "module", "http", "error", err)
	}
}
