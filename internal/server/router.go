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
	AppEnv                    string
	ServiceName               string
	Logger                    *slog.Logger
	MySQLCheck                DependencyCheck
	RedisCheck                DependencyCheck
	RegisterHandler           gin.HandlerFunc
	LoginHandler              gin.HandlerFunc
	AuthMiddleware            gin.HandlerFunc
	GetProfileHandler         gin.HandlerFunc
	UpdateProfileHandler      gin.HandlerFunc
	ListSourcesHandler        gin.HandlerFunc
	GetSourceHandler          gin.HandlerFunc
	CreateSubscriptionHandler gin.HandlerFunc
	ListSubscriptionsHandler  gin.HandlerFunc
	GetSubscriptionHandler    gin.HandlerFunc
	UpdateSubscriptionHandler gin.HandlerFunc
	DeleteSubscriptionHandler gin.HandlerFunc
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

	apiV1 := router.Group("/api/v1")
	registerAPIV1Routes(
		apiV1,
		dependencies.RegisterHandler,
		dependencies.LoginHandler,
		dependencies.AuthMiddleware,
		dependencies.GetProfileHandler,
		dependencies.UpdateProfileHandler,
		dependencies.ListSourcesHandler,
		dependencies.GetSourceHandler,
		dependencies.CreateSubscriptionHandler,
		dependencies.ListSubscriptionsHandler,
		dependencies.GetSubscriptionHandler,
		dependencies.UpdateSubscriptionHandler,
		dependencies.DeleteSubscriptionHandler,
	)

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

	if dependencies.RegisterHandler == nil {
		return errors.New("register handler is required")
	}
	if dependencies.LoginHandler == nil {
		return errors.New("login handler is required")
	}
	if dependencies.AuthMiddleware == nil {
		return errors.New("auth middleware is required")
	}
	if dependencies.GetProfileHandler == nil {
		return errors.New("get profile handler is required")
	}
	if dependencies.UpdateProfileHandler == nil {
		return errors.New("update profile handler is required")
	}
	if dependencies.ListSourcesHandler == nil {
		return errors.New("list sources handler is required")
	}
	if dependencies.GetSourceHandler == nil {
		return errors.New("get source handler is required")
	}
	if dependencies.CreateSubscriptionHandler == nil {
		return errors.New("create subscription handler is required")
	}
	if dependencies.ListSubscriptionsHandler == nil {
		return errors.New("list subscriptions handler is required")
	}
	if dependencies.GetSubscriptionHandler == nil {
		return errors.New("get subscription handler is required")
	}
	if dependencies.UpdateSubscriptionHandler == nil {
		return errors.New("update subscription handler is required")
	}
	if dependencies.DeleteSubscriptionHandler == nil {
		return errors.New("delete subscription handler is required")
	}

	return nil
}

// registerAPIV1Routes 是后续 M1 业务接口的统一注册位置。
func registerAPIV1Routes(
	apiV1 *gin.RouterGroup,
	registerHandler gin.HandlerFunc,
	loginHandler gin.HandlerFunc,
	authMiddleware gin.HandlerFunc,
	getProfileHandler gin.HandlerFunc,
	updateProfileHandler gin.HandlerFunc,
	listSourcesHandler gin.HandlerFunc,
	getSourceHandler gin.HandlerFunc,
	createSubscriptionHandler gin.HandlerFunc,
	listSubscriptionsHandler gin.HandlerFunc,
	getSubscriptionHandler gin.HandlerFunc,
	updateSubscriptionHandler gin.HandlerFunc,
	deleteSubscriptionHandler gin.HandlerFunc,
) {
	auth := apiV1.Group("/auth")
	auth.POST("/register", registerHandler)
	auth.POST("/login", loginHandler)

	protectedAuth := auth.Group("")
	protectedAuth.Use(authMiddleware)
	protectedAuth.GET("/probe", func(c *gin.Context) {
		userID, _ := httpx.CurrentUserID(c)
		c.JSON(http.StatusOK, gin.H{"user_id": userID})
	})

	protected := apiV1.Group("")
	protected.Use(authMiddleware)
	protected.GET("/me", getProfileHandler)
	protected.PATCH("/me", updateProfileHandler)
	protected.GET("/sources", listSourcesHandler)
	protected.GET("/sources/:id", getSourceHandler)
	protected.POST("/subscriptions", createSubscriptionHandler)
	protected.GET("/subscriptions", listSubscriptionsHandler)
	protected.GET("/subscriptions/:id", getSubscriptionHandler)
	protected.PATCH("/subscriptions/:id", updateSubscriptionHandler)
	protected.DELETE("/subscriptions/:id", deleteSubscriptionHandler)
}
