package config

import (
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"
)

const developmentJWTSecret = "development-only-change-me-32-bytes"

type Config struct {
	AppEnv          string        // 应用运行环境，例如 dev、test、prod
	HTTPAddr        string        // HTTP 服务监听地址，例如 ":8080"
	LogLevel        string        // 日志级别，例如 DEBUG、INFO、WARN、ERROR
	WorkerHeartbeat time.Duration // Worker 心跳间隔，例如 10s、30s

	MySQLDSN          string // MySQL 数据库连接字符串，"user:password@tcp(127.0.0.1:3306)/dbname"
	MySQLMaxOpenConns int    // MySQL 最大允许打开的数据库连接数
	MySQLMaxIdleConns int    // MySQL 连接池中最大空闲连接数

	RedisAddr     string // Redis 服务地址，例如 "127.0.0.1:6379"
	RedisPassword string // Redis 连接密码
	RedisDB       int    // 使用的 Redis 数据库编号，例如 0、1、2

	JWTSecret string
	JWTTTL    time.Duration
	JWTIssuer string
}

// 把环境变量的名字集中管理，避免项目中到处直接写字符串
const (
	envAppEnv            = "APP_ENV"
	envHTTPAddr          = "HTTP_ADDR"
	envLogLevel          = "LOG_LEVEL"
	envWorkerHeartbeat   = "WORKER_HEARTBEAT"
	envMySQLDSN          = "MYSQL_DSN"
	envMySQLMaxOpenConns = "MYSQL_MAX_OPEN_CONNS"
	envMySQLMaxIdleConns = "MYSQL_MAX_IDLE_CONNS"
	envRedisAddr         = "REDIS_ADDR"
	envRedisPassword     = "REDIS_PASSWORD"
	envRedisDB           = "REDIS_DB"
	envJWTSecret         = "JWT_SECRET"
	envJWTTTL            = "JWT_TTL"
	envJWTIssuer         = "JWT_ISSUER"
)

/*
功能：读取、解析并校验应用运行所需的全部环境变量。
参数：无。
返回值：校验通过的 Config；配置缺失或非法时返回错误。
*/
func Load() (Config, error) {
	appEnv, err := requiredEnv(envAppEnv)
	if err != nil {
		return Config{}, err
	}

	httpAddr, err := requiredEnv(envHTTPAddr)
	if err != nil {
		return Config{}, err
	}

	logLevel, err := requiredEnv(envLogLevel)
	if err != nil {
		return Config{}, err
	}

	rawHeartbeat, err := requiredEnv(envWorkerHeartbeat)
	if err != nil {
		return Config{}, err
	}

	heartbeat, err := time.ParseDuration(rawHeartbeat)
	if err != nil {
		return Config{}, fmt.Errorf("invalid %s: %w", envWorkerHeartbeat, err)
	}

	mysqlDSN, err := requiredEnv(envMySQLDSN)
	if err != nil {
		return Config{}, err
	}
	mysqlMaxOpenConns, err := requiredInt(envMySQLMaxOpenConns)
	if err != nil {
		return Config{}, err
	}
	mysqlMaxIdleConns, err := requiredInt(envMySQLMaxIdleConns)
	if err != nil {
		return Config{}, err
	}
	redisAddr, err := requiredEnv(envRedisAddr)
	if err != nil {
		return Config{}, err
	}
	redisPassword, err := requiredEnv(envRedisPassword)
	if err != nil {
		return Config{}, err
	}
	redisDB, err := requiredInt(envRedisDB)
	if err != nil {
		return Config{}, err
	}
	jwtSecret, err := requiredEnv(envJWTSecret)
	if err != nil {
		return Config{}, err
	}
	rawJWTTTL, err := requiredEnv(envJWTTTL)
	if err != nil {
		return Config{}, err
	}
	jwtTTL, err := time.ParseDuration(rawJWTTTL)
	if err != nil {
		return Config{}, fmt.Errorf("invalid %s: must be a duration", envJWTTTL)
	}
	jwtIssuer, err := requiredEnv(envJWTIssuer)
	if err != nil {
		return Config{}, err
	}
	cfg := Config{
		AppEnv:            appEnv,
		HTTPAddr:          httpAddr,
		LogLevel:          logLevel,
		WorkerHeartbeat:   heartbeat,
		MySQLDSN:          mysqlDSN,
		MySQLMaxOpenConns: mysqlMaxOpenConns,
		MySQLMaxIdleConns: mysqlMaxIdleConns,
		RedisAddr:         redisAddr,
		RedisPassword:     redisPassword,
		RedisDB:           redisDB,
		JWTSecret:         jwtSecret,
		JWTTTL:            jwtTTL,
		JWTIssuer:         jwtIssuer,
	}

	if err := validate(cfg); err != nil {
		return Config{}, err
	}

	return cfg, nil
}

/*
功能：读取一个必填环境变量。
参数：key 为环境变量名称。
返回值：环境变量值；变量不存在或为空时返回错误。
*/
func requiredEnv(key string) (string, error) {
	value, ok := os.LookupEnv(key)
	if !ok || value == "" {
		return "", fmt.Errorf("missing required env: %s", key)
	}
	return value, nil
}

/*
功能：校验 Config 中各字段的枚举值、地址格式与数值范围。
参数：cfg 为待校验的完整配置。
返回值：配置合法时返回 nil；否则返回不包含秘密值的错误。
*/
func validate(cfg Config) error {
	// 校验 cfg.AppEnv
	switch cfg.AppEnv {
	case "development", "test", "production":
	default:
		return fmt.Errorf("invalid APP_ENV: %s", cfg.AppEnv)
	}

	//校验 cfg.LogLevel。
	switch cfg.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		return fmt.Errorf("invalid LOG_LEVEL: %s", cfg.LogLevel)
	}
	// 校验 cfg.HTTPAddr，检查 host 和 port 都不为空。
	host, post, err := net.SplitHostPort(cfg.HTTPAddr)
	if err != nil || host == "" || post == "" {
		return fmt.Errorf("invalid HTTP_ADDR: must include host and port")
	}
	//校验 cfg.WorkerHeartbeat。
	if cfg.WorkerHeartbeat <= 0 {
		return fmt.Errorf("invalid WORKER_HEARTBEAT: must be greater than 0")
	}

	if cfg.MySQLMaxOpenConns <= 0 {
		return fmt.Errorf("invalid MYSQL_MAX_OPEN_CONNS: must be greater than 0")
	}

	if cfg.MySQLMaxIdleConns < 0 {
		return fmt.Errorf("invalid MYSQL_MAX_IDLE_CONNS: must not be negative")
	}

	if cfg.MySQLMaxIdleConns > cfg.MySQLMaxOpenConns {
		return fmt.Errorf("invalid MYSQL_MAX_IDLE_CONNS: must not exceed MYSQL_MAX_OPEN_CONNS")
	}

	if cfg.RedisDB < 0 {
		return fmt.Errorf("invalid REDIS_DB: must not be negative")
	}

	if len(cfg.JWTSecret) < 32 {
		return fmt.Errorf("invalid JWT_SECRET: must be at least 32 bytes")
	}
	if cfg.AppEnv == "production" && cfg.JWTSecret == developmentJWTSecret {
		return fmt.Errorf("invalid JWT_SECRET: development placeholder is not allowed in production")
	}
	if cfg.JWTTTL <= 0 || cfg.JWTTTL > 24*time.Hour {
		return fmt.Errorf("invalid JWT_TTL: must be greater than 0 and at most 24h")
	}
	if strings.TrimSpace(cfg.JWTIssuer) == "" {
		return fmt.Errorf("invalid JWT_ISSUER: must not be blank")
	}

	return nil
}

/*
功能：读取并解析一个必填整数环境变量。
参数：key 为环境变量名称。
返回值：解析后的整数；变量缺失或不是整数时返回错误。
*/
func requiredInt(key string) (int, error) {
	raw, err := requiredEnv(key)
	if err != nil {
		return 0, err
	}

	value, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("invalid %s: must be an integer", key)
	}
	return value, nil
}
