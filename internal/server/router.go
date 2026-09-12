package server

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"signalwatch/internal/platform/httpx"
	"signalwatch/web"
)

// Dependencies 包含创建 HTTP Router 所需的全部依赖。
type Dependencies struct {
	AppEnv        string
	ServiceName   string
	Logger        *slog.Logger
	MySQLCheck    DependencyCheck
	RedisCheck    DependencyCheck
	AgentHandler  gin.HandlerFunc
	AI            AIRoutes
	Accounts      AccountsRoutes
	Sources       SourcesRoutes
	Subscriptions SubscriptionsRoutes
	Papers        PapersRoutes
	Operations    OperationsRoutes
	Authorization AuthorizationRoutes
}
type AIRoutes struct {
	CredentialsHandler           gin.HandlerFunc
	CredentialHandler            gin.HandlerFunc
	DefaultSelectionHandler      gin.HandlerFunc
	GetAISummaryHandler          gin.HandlerFunc
	RequestAISummaryHandler      gin.HandlerFunc
	ListAIProvidersHandler       gin.HandlerFunc
	GetAIConfigurationHandler    gin.HandlerFunc
	PutAIConfigurationHandler    gin.HandlerFunc
	PatchAIConfigurationHandler  gin.HandlerFunc
	RotateAISecretHandler        gin.HandlerFunc
	TestAIConfigurationHandler   gin.HandlerFunc
	DeleteAIConfigurationHandler gin.HandlerFunc
	GetAIUsageHandler            gin.HandlerFunc
	ListAICallsHandler           gin.HandlerFunc
}
type AccountsRoutes struct {
	RegisterHandler      gin.HandlerFunc
	LoginHandler         gin.HandlerFunc
	RefreshHandler       gin.HandlerFunc
	LogoutHandler        gin.HandlerFunc
	GetProfileHandler    gin.HandlerFunc
	UpdateProfileHandler gin.HandlerFunc
}
type SourcesRoutes struct {
	ListSourcesHandler gin.HandlerFunc
	GetSourceHandler   gin.HandlerFunc
}
type SubscriptionsRoutes struct {
	CreateSubscriptionHandler gin.HandlerFunc
	ListSubscriptionsHandler  gin.HandlerFunc
	GetSubscriptionHandler    gin.HandlerFunc
	UpdateSubscriptionHandler gin.HandlerFunc
	DeleteSubscriptionHandler gin.HandlerFunc
}
type PapersRoutes struct {
	ListPapersHandler gin.HandlerFunc
	GetPaperHandler   gin.HandlerFunc
}
type OperationsRoutes struct {
	OperationsStatusHandler  gin.HandlerFunc
	OperationsSourcesHandler gin.HandlerFunc
}
type AuthorizationRoutes struct {
	AuthMiddleware            gin.HandlerFunc
	ActiveRoleMiddleware      gin.HandlerFunc
	UserRoleMiddleware        gin.HandlerFunc
	OperatorRoleMiddleware    gin.HandlerFunc
	OperationsAuditMiddleware gin.HandlerFunc
}

// NewRouter 创建并配置 SignalWatch API 的 Gin Router。
func NewRouter(dependencies Dependencies) (*gin.Engine, error) {
	if err := validateDependencies(dependencies); err != nil {
		return nil, err
	}
	//gin.SetMode 不是某个 Router 的配置，而是整个进程的全局配置
	setGinMode(dependencies.AppEnv)

	router := gin.New()

	// Gin 默认会自动重定向 /healthz/ 到 /healthz。
	// 关闭重定向后，所有未知路径都进入统一 JSON 404。
	router.RedirectTrailingSlash = false
	router.RedirectFixedPath = false

	// 启用方法检查，POST /healthz 才会进入 NoMethod，
	// 返回 405 而不是 404。
	router.HandleMethodNotAllowed = true

	// 当前部署不依赖可信反向代理，因此不接受客户端通过
	// X-Forwarded-For 伪造来源 IP。
	if err := router.SetTrustedProxies(nil); err != nil {
		return nil, fmt.Errorf("disable trusted proxies: %w", err)
	}

	// 注册顺序就是请求的执行顺序。
	router.Use(
		httpx.RequestIDMiddleware(),
		requestBudget(),
		httpx.RecoveryMiddleware(dependencies.Logger),
		httpx.AccessLogMiddleware(dependencies.Logger),
	)

	router.NoRoute(func(c *gin.Context) {
		httpx.WriteError(
			c,
			http.StatusNotFound,
			httpx.CodeNotFound,
			"route not found",
		)
	})

	router.NoMethod(func(c *gin.Context) {
		httpx.WriteError(
			c,
			http.StatusMethodNotAllowed,
			httpx.CodeMethodNotAllowed,
			"method not allowed",
		)
	})

	router.GET(
		"/healthz",
		healthHandler(dependencies.ServiceName),
	)

	router.GET(
		"/readyz",
		readinessHandler(
			dependencies.Logger,
			dependencies.MySQLCheck,
			dependencies.RedisCheck,
		),
	)

	apiV2 := router.Group("/api/v2")
	registerAPIV2Routes(apiV2, dependencies)

	aiRoutes := apiV2.Group("")
	aiRoutes.Use(dependencies.Authorization.AuthMiddleware, dependencies.Authorization.ActiveRoleMiddleware, dependencies.Authorization.UserRoleMiddleware)
	disabled := func(c *gin.Context) { httpx.WriteError(c, 503, "AI_DISABLED", "AI enrichment is disabled") }
	getAI, requestAI := dependencies.AI.GetAISummaryHandler, dependencies.AI.RequestAISummaryHandler
	if getAI == nil {
		getAI = disabled
	}
	if requestAI == nil {
		requestAI = disabled
	}
	aiRoutes.GET("/papers/:id/ai-summary", getAI)
	aiRoutes.POST("/papers/:id/ai-summary", requestAI)
	orDisabled := func(handler gin.HandlerFunc) gin.HandlerFunc {
		if handler == nil {
			return disabled
		}
		return handler
	}
	agentHandler := orDisabled(dependencies.AgentHandler)
	aiRoutes.GET("/agent/conversations", agentHandler)
	aiRoutes.POST("/agent/conversations", agentHandler)
	aiRoutes.GET("/agent/conversations/:id", agentHandler)
	aiRoutes.DELETE("/agent/conversations/:id", agentHandler)
	aiRoutes.GET("/agent/conversations/:id/messages", agentHandler)
	aiRoutes.POST("/agent/conversations/:id/messages", agentHandler)
	aiRoutes.GET("/agent/runs/:id", agentHandler)
	aiRoutes.POST("/agent/runs/:id/cancel", agentHandler)
	aiRoutes.GET("/agent/subscription-drafts/:id", agentHandler)
	aiRoutes.PATCH("/agent/subscription-drafts/:id", agentHandler)
	aiRoutes.POST("/agent/subscription-drafts/:id/confirm", agentHandler)
	aiRoutes.GET("/ai/credentials", orDisabled(dependencies.AI.CredentialsHandler))
	aiRoutes.POST("/ai/credentials", orDisabled(dependencies.AI.CredentialsHandler))
	aiRoutes.GET("/ai/credentials/:id", orDisabled(dependencies.AI.CredentialHandler))
	aiRoutes.PUT("/ai/credentials/:id", orDisabled(dependencies.AI.CredentialHandler))
	aiRoutes.DELETE("/ai/credentials/:id", orDisabled(dependencies.AI.CredentialHandler))
	aiRoutes.POST("/ai/credentials/:id/test", orDisabled(dependencies.AI.CredentialHandler))
	aiRoutes.GET("/ai/default-selection", orDisabled(dependencies.AI.DefaultSelectionHandler))
	aiRoutes.PUT("/ai/default-selection", orDisabled(dependencies.AI.DefaultSelectionHandler))
	aiRoutes.GET("/ai/providers", orDisabled(dependencies.AI.ListAIProvidersHandler))
	aiRoutes.GET("/ai/configuration", orDisabled(dependencies.AI.GetAIConfigurationHandler))
	aiRoutes.PUT("/ai/configuration", orDisabled(dependencies.AI.PutAIConfigurationHandler))
	aiRoutes.PATCH("/ai/configuration", orDisabled(dependencies.AI.PatchAIConfigurationHandler))
	aiRoutes.PUT("/ai/configuration/secret", orDisabled(dependencies.AI.RotateAISecretHandler))
	aiRoutes.POST("/ai/configuration/test", orDisabled(dependencies.AI.TestAIConfigurationHandler))
	aiRoutes.DELETE("/ai/configuration", orDisabled(dependencies.AI.DeleteAIConfigurationHandler))
	aiRoutes.GET("/ai/calls", orDisabled(dependencies.AI.ListAICallsHandler))
	aiRoutes.GET("/ai/usage", orDisabled(dependencies.AI.GetAIUsageHandler))
	web.RegisterRoutes(router)

	return router, nil
}

func setGinMode(appEnv string) {
	switch appEnv {
	case "development":
		gin.SetMode(gin.DebugMode)
	case "test":
		gin.SetMode(gin.TestMode)
	case "production":
		gin.SetMode(gin.ReleaseMode)
	}
}

func validateDependencies(dependencies Dependencies) error {
	switch dependencies.AppEnv {
	case "development", "test", "production":
	default:
		return fmt.Errorf(
			"unsupported app environment %q",
			dependencies.AppEnv,
		)
	}

	if strings.TrimSpace(dependencies.ServiceName) == "" {
		return errors.New("service name is required")
	}

	if dependencies.Logger == nil {
		return errors.New("logger is required")
	}

	if dependencies.MySQLCheck == nil {
		return errors.New("mysql dependency check is required")
	}

	if dependencies.RedisCheck == nil {
		return errors.New("redis dependency check is required")
	}

	if dependencies.Accounts.RegisterHandler == nil {
		return errors.New("register handler is required")
	}
	if dependencies.Accounts.LoginHandler == nil {
		return errors.New("login handler is required")
	}
	if dependencies.Accounts.RefreshHandler == nil || dependencies.Accounts.LogoutHandler == nil {
		return errors.New("session handlers are required")
	}
	if dependencies.Authorization.AuthMiddleware == nil {
		return errors.New("auth middleware is required")
	}
	if dependencies.Authorization.ActiveRoleMiddleware == nil || dependencies.Authorization.UserRoleMiddleware == nil ||
		dependencies.Authorization.OperatorRoleMiddleware == nil || dependencies.Authorization.OperationsAuditMiddleware == nil {
		return errors.New("role and operations middleware are required")
	}
	if dependencies.Accounts.GetProfileHandler == nil {
		return errors.New("get profile handler is required")
	}
	if dependencies.Accounts.UpdateProfileHandler == nil {
		return errors.New("update profile handler is required")
	}
	if dependencies.Sources.ListSourcesHandler == nil {
		return errors.New("list sources handler is required")
	}
	if dependencies.Sources.GetSourceHandler == nil {
		return errors.New("get source handler is required")
	}
	if dependencies.Subscriptions.CreateSubscriptionHandler == nil {
		return errors.New("create subscription handler is required")
	}
	if dependencies.Subscriptions.ListSubscriptionsHandler == nil {
		return errors.New("list subscriptions handler is required")
	}
	if dependencies.Subscriptions.GetSubscriptionHandler == nil {
		return errors.New("get subscription handler is required")
	}
	if dependencies.Subscriptions.UpdateSubscriptionHandler == nil {
		return errors.New("update subscription handler is required")
	}
	if dependencies.Subscriptions.DeleteSubscriptionHandler == nil {
		return errors.New("delete subscription handler is required")
	}
	if dependencies.Papers.ListPapersHandler == nil {
		return errors.New("list papers handler is required")
	}
	if dependencies.Papers.GetPaperHandler == nil {
		return errors.New("get paper handler is required")
	}
	if dependencies.Operations.OperationsStatusHandler == nil || dependencies.Operations.OperationsSourcesHandler == nil {
		return errors.New("operations handlers are required")
	}

	return nil
}

// registerAPIV2Routes groups module routes behind their authorization boundary.
func registerAPIV2Routes(apiV2 *gin.RouterGroup, d Dependencies) {
	auth := apiV2.Group("/auth")
	auth.POST("/register", d.Accounts.RegisterHandler)
	auth.POST("/login", d.Accounts.LoginHandler)
	auth.POST("/refresh", d.Accounts.RefreshHandler)
	auth.POST("/logout", d.Accounts.LogoutHandler)

	protectedAuth := auth.Group("")
	protectedAuth.Use(d.Authorization.AuthMiddleware, d.Authorization.ActiveRoleMiddleware)
	protectedAuth.GET("/probe", func(c *gin.Context) {
		userID, _ := httpx.CurrentUserID(c)
		c.JSON(http.StatusOK, gin.H{"user_id": userID})
	})

	protected := apiV2.Group("")
	protected.Use(d.Authorization.AuthMiddleware, d.Authorization.UserRoleMiddleware)
	protected.GET("/me", d.Accounts.GetProfileHandler)
	protected.PATCH("/me", d.Accounts.UpdateProfileHandler)
	protected.GET("/sources", d.Sources.ListSourcesHandler)
	protected.GET("/sources/:id", d.Sources.GetSourceHandler)
	protected.POST("/subscriptions", d.Subscriptions.CreateSubscriptionHandler)
	protected.GET("/subscriptions", d.Subscriptions.ListSubscriptionsHandler)
	protected.GET("/subscriptions/:id", d.Subscriptions.GetSubscriptionHandler)
	protected.PATCH("/subscriptions/:id", d.Subscriptions.UpdateSubscriptionHandler)
	protected.DELETE("/subscriptions/:id", d.Subscriptions.DeleteSubscriptionHandler)
	protected.GET("/papers", d.Papers.ListPapersHandler)
	protected.GET("/papers/:id", d.Papers.GetPaperHandler)

	operations := apiV2.Group("/ops")
	operations.Use(d.Authorization.AuthMiddleware, d.Authorization.OperatorRoleMiddleware, d.Authorization.OperationsAuditMiddleware)
	operations.GET("/status", d.Operations.OperationsStatusHandler)
	operations.GET("/sources", d.Operations.OperationsSourcesHandler)
}
