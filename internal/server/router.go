package server

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"signalwatch/internal/platform/httpx"
)

// Dependencies 包含创建 HTTP Router 所需的全部依赖。
type Dependencies struct {
	AppEnv          string
	ServiceName     string
	Logger          *slog.Logger
	MySQLCheck      DependencyCheck
	RedisCheck      DependencyCheck
	RegisterHandler gin.HandlerFunc
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
	registerAPIV1Routes(apiV1, dependencies.RegisterHandler)

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

	return nil
}

// registerAPIV1Routes 是后续 M1 业务接口的统一注册位置。
func registerAPIV1Routes(
	apiV1 *gin.RouterGroup,
	registerHandler gin.HandlerFunc,
) {
	auth := apiV1.Group("/auth")
	auth.POST("/register", registerHandler)
	//实际路径，/api/v1/auth/register
}
