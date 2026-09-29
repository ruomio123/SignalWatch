package config

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/mail"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"signalwatch/internal/generation"
	"signalwatch/internal/platform/llm"
	"signalwatch/internal/platform/secret"
)

const developmentJWTSecret = "development-only-change-me-32-bytes"

type Config struct {
	AIModelLimits                 map[string]generation.ModelLimits
	AIPaperCallTimeout            time.Duration
	AIEnabled                     bool
	AIConfigTestMinInterval       time.Duration
	AIGenerationMinInterval       time.Duration
	AIConfigTestDailyLimit        int
	AIPaperDailyLimit             int
	AIDigestDailyLimit            int
	AISubscriptionAgentDailyLimit int
	AIPaperQADailyLimit           int
	AICredentialKeys              string
	AICredentialActiveKeyVersion  string
	AIEnabledProviders            []string
	AIWorkers                     int
	AIQueueCapacity               int

	AppEnv          string        // 应用运行环境，例如 dev、test、prod
	PublicBaseURL   string        // Optional public origin for email links
	HTTPAddr        string        // HTTP 服务监听地址，例如 ":8080"
	LogLevel        string        // 日志级别，例如 DEBUG、INFO、WARN、ERROR
	WorkerHeartbeat time.Duration // Worker 心跳间隔，例如 10s、30s

	MySQLDSN          string // MySQL 数据库连接字符串，"user:password@tcp(127.0.0.1:3306)/dbname"
	MySQLMaxOpenConns int    // MySQL 最大允许打开的数据库连接数
	MySQLMaxIdleConns int    // MySQL 连接池中最大空闲连接数

	RedisAddr     string // Redis 服务地址，例如 "127.0.0.1:6379"
	RedisPassword string // Redis 连接密码
	RedisDB       int    // 使用的 Redis 数据库编号，例如 0、1、2

	JWTSecret      string
	JWTTTL         time.Duration
	AuthSessionTTL time.Duration
	JWTIssuer      string

	CollectorLockTTL       time.Duration
	ArXivBootstrapLookback time.Duration
	ArXivRecoveryOverlap   time.Duration
	ArXivDailySyncTime     string
	ArXivSyncRetryInterval time.Duration
	ArXivFeedEndpoint      string
	ArXivPageSize          int
	ArXivMaxPages          int
	ArXivMaxResponseBytes  int64
	ArXivRequestAttempts   int
	ArXivRequestBackoff    time.Duration
	ArXivRequestInterval   time.Duration
	ArXivHTTPTimeout       time.Duration
	MatcherWorkers         int
	MatcherQueueCapacity   int
	OpsStatusRetention     time.Duration

	DigestInterval    time.Duration
	MailWorkers       int
	MailQueueCapacity int
	SMTPAddr          string
	SMTPFrom          string
	SMTPUsername      string
	SMTPPassword      string
	SMTPStartTLS      bool
	SMTPTimeout       time.Duration
}

// 把环境变量的名字集中管理，避免项目中到处直接写字符串
const (
	envAppEnv                 = "APP_ENV"
	envHTTPAddr               = "HTTP_ADDR"
	envLogLevel               = "LOG_LEVEL"
	envWorkerHeartbeat        = "WORKER_HEARTBEAT"
	envMySQLDSN               = "MYSQL_DSN"
	envMySQLMaxOpenConns      = "MYSQL_MAX_OPEN_CONNS"
	envMySQLMaxIdleConns      = "MYSQL_MAX_IDLE_CONNS"
	envRedisAddr              = "REDIS_ADDR"
	envRedisPassword          = "REDIS_PASSWORD"
	envRedisDB                = "REDIS_DB"
	envJWTSecret              = "JWT_SECRET"
	envJWTTTL                 = "JWT_TTL"
	envJWTIssuer              = "JWT_ISSUER"
	envCollectorLockTTL       = "COLLECTOR_LOCK_TTL"
	envArXivBootstrapLookback = "ARXIV_BOOTSTRAP_LOOKBACK"
	envArXivRecoveryOverlap   = "ARXIV_RECOVERY_OVERLAP"
	envArXivDailySyncTime     = "ARXIV_DAILY_SYNC_TIME"
	envArXivSyncRetryInterval = "ARXIV_SYNC_RETRY_INTERVAL"
	envArXivFeedEndpoint      = "ARXIV_FEED_ENDPOINT"
	envArXivPageSize          = "ARXIV_PAGE_SIZE"
	envArXivMaxPages          = "ARXIV_MAX_PAGES"
	envArXivMaxResponseBytes  = "ARXIV_MAX_RESPONSE_BYTES"
	envArXivRequestAttempts   = "ARXIV_REQUEST_ATTEMPTS"
	envArXivRequestBackoff    = "ARXIV_REQUEST_BACKOFF"
	envArXivRequestInterval   = "ARXIV_REQUEST_INTERVAL"
	envArXivHTTPTimeout       = "ARXIV_HTTP_TIMEOUT"
	envMatcherWorkers         = "MATCHER_WORKERS"
	envMatcherQueueCapacity   = "MATCHER_QUEUE_CAPACITY"
	envOpsStatusRetention     = "OPS_STATUS_RETENTION"
	envDigestInterval         = "DIGEST_INTERVAL"
	envMailWorkers            = "MAIL_WORKERS"
	envMailQueueCapacity      = "MAIL_QUEUE_CAPACITY"
	envSMTPAddr               = "SMTP_ADDR"
	envSMTPFrom               = "SMTP_FROM"
	envSMTPUsername           = "SMTP_USERNAME"
	envSMTPPassword           = "SMTP_PASSWORD"
	envSMTPStartTLS           = "SMTP_STARTTLS"
	envSMTPTimeout            = "SMTP_TIMEOUT"
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
	rawSessionTTL := stringEnvOrDefault("AUTH_SESSION_TTL", "168h")
	sessionTTL, err := time.ParseDuration(rawSessionTTL)
	if err != nil {
		return Config{}, fmt.Errorf("invalid AUTH_SESSION_TTL: must be a duration")
	}
	jwtIssuer, err := requiredEnv(envJWTIssuer)
	if err != nil {
		return Config{}, err
	}
	smtpStartTLS, err := boolEnvOrDefault(envSMTPStartTLS, false)
	if err != nil {
		return Config{}, err
	}
	aiEnabled, err := boolEnvOrDefault("AI_ENABLED", false)
	if err != nil {
		return Config{}, err
	}
	cfg := Config{
		AIPaperCallTimeout:      time.Minute,
		AIEnabled:               aiEnabled,
		AIConfigTestMinInterval: 10 * time.Second, AIGenerationMinInterval: 2 * time.Second,
		AICredentialKeys:             os.Getenv("AI_CREDENTIAL_KEYS"),
		AICredentialActiveKeyVersion: strings.TrimSpace(os.Getenv("AI_CREDENTIAL_ACTIVE_KEY_VERSION")),
		AIEnabledProviders:           splitCSV(os.Getenv("AI_ENABLED_PROVIDERS")),
		AIWorkers:                    intEnvOrDefault("AI_WORKERS", 2), AIQueueCapacity: intEnvOrDefault("AI_QUEUE_CAPACITY", 128),
		AppEnv:                 appEnv,
		HTTPAddr:               httpAddr,
		PublicBaseURL:          strings.TrimRight(strings.TrimSpace(os.Getenv("APP_PUBLIC_URL")), "/"),
		LogLevel:               logLevel,
		WorkerHeartbeat:        heartbeat,
		MySQLDSN:               mysqlDSN,
		MySQLMaxOpenConns:      mysqlMaxOpenConns,
		MySQLMaxIdleConns:      mysqlMaxIdleConns,
		RedisAddr:              redisAddr,
		RedisPassword:          redisPassword,
		RedisDB:                redisDB,
		JWTSecret:              jwtSecret,
		JWTTTL:                 jwtTTL,
		AuthSessionTTL:         sessionTTL,
		JWTIssuer:              jwtIssuer,
		CollectorLockTTL:       durationEnvOrDefault(envCollectorLockTTL, 55*time.Minute),
		ArXivBootstrapLookback: durationEnvOrDefault(envArXivBootstrapLookback, 7*24*time.Hour),
		ArXivRecoveryOverlap:   durationEnvOrDefault(envArXivRecoveryOverlap, 24*time.Hour),
		ArXivDailySyncTime:     stringEnvOrDefault(envArXivDailySyncTime, "00:30"),
		ArXivSyncRetryInterval: durationEnvOrDefault(envArXivSyncRetryInterval, 15*time.Minute),
		ArXivFeedEndpoint:      stringEnvOrDefault(envArXivFeedEndpoint, "https://rss.arxiv.org/atom"),
		ArXivPageSize:          intEnvOrDefault(envArXivPageSize, 100),
		ArXivMaxPages:          intEnvOrDefault(envArXivMaxPages, 10),
		ArXivMaxResponseBytes:  int64EnvOrDefault(envArXivMaxResponseBytes, 5<<20),
		ArXivRequestAttempts:   intEnvOrDefault(envArXivRequestAttempts, 3),
		ArXivRequestBackoff:    durationEnvOrDefault(envArXivRequestBackoff, 5*time.Second),
		ArXivRequestInterval:   durationEnvOrDefault(envArXivRequestInterval, 3*time.Second),
		ArXivHTTPTimeout:       durationEnvOrDefault(envArXivHTTPTimeout, 30*time.Second),
		MatcherWorkers:         intEnvOrDefault(envMatcherWorkers, 4),
		MatcherQueueCapacity:   intEnvOrDefault(envMatcherQueueCapacity, 256),
		OpsStatusRetention:     durationEnvOrDefault(envOpsStatusRetention, 168*time.Hour),
		DigestInterval:         durationEnvOrDefault(envDigestInterval, time.Minute),
		MailWorkers:            intEnvOrDefault(envMailWorkers, 2),
		MailQueueCapacity:      intEnvOrDefault(envMailQueueCapacity, 128),
		SMTPAddr:               stringEnvOrDefault(envSMTPAddr, "127.0.0.1:1025"),
		SMTPFrom:               stringEnvOrDefault(envSMTPFrom, "SignalWatch <digest@signalwatch.local>"),
		SMTPUsername:           strings.TrimSpace(os.Getenv(envSMTPUsername)),
		SMTPPassword:           os.Getenv(envSMTPPassword),
		SMTPStartTLS:           smtpStartTLS,
		SMTPTimeout:            durationEnvOrDefault(envSMTPTimeout, 10*time.Second),
	}

	if cfg.AIPaperCallTimeout, err = parsePaperCallTimeout(os.Getenv("AI_PAPER_CALL_TIMEOUT")); err != nil {
		return cfg, err
	}
	cfg.AIModelLimits, err = parseModelLimits(os.Getenv("AI_MODEL_LIMITS_JSON"))
	if err != nil {
		return cfg, err
	}

	for _, option := range []struct {
		name   string
		target *time.Duration
	}{{"AI_CONFIG_TEST_MIN_INTERVAL", &cfg.AIConfigTestMinInterval}, {"AI_GENERATION_MIN_INTERVAL", &cfg.AIGenerationMinInterval}} {
		if raw, ok := os.LookupEnv(option.name); ok {
			v, e := time.ParseDuration(raw)
			if e != nil || v < time.Second || v > time.Hour {
				return cfg, fmt.Errorf("invalid %s", option.name)
			}
			*option.target = v
		}
	}
	for _, option := range []struct {
		name   string
		target *int
	}{{"AI_CONFIG_TEST_DAILY_LIMIT", &cfg.AIConfigTestDailyLimit}, {"AI_PAPER_DAILY_LIMIT", &cfg.AIPaperDailyLimit}, {"AI_DIGEST_DAILY_LIMIT", &cfg.AIDigestDailyLimit}, {"AI_SUBSCRIPTION_AGENT_DAILY_LIMIT", &cfg.AISubscriptionAgentDailyLimit}, {"AI_PAPER_QA_DAILY_LIMIT", &cfg.AIPaperQADailyLimit}} {
		if raw, ok := os.LookupEnv(option.name); ok {
			v, e := strconv.Atoi(raw)
			if e != nil || v < 0 || v > 1000000 {
				return cfg, fmt.Errorf("invalid %s", option.name)
			}
			*option.target = v
		}
	}
	if err := validate(cfg); err != nil {
		return Config{}, err
	}

	return cfg, nil
}

func parsePaperCallTimeout(raw string) (time.Duration, error) {
	if raw == "" {
		return time.Minute, nil
	}
	value, err := time.ParseDuration(raw)
	if err != nil || value < 10*time.Second || value > 120*time.Second {
		return 0, fmt.Errorf("invalid AI_PAPER_CALL_TIMEOUT")
	}
	return value, nil
}

func parseModelLimits(raw string) (map[string]generation.ModelLimits, error) {
	limits := map[string]generation.ModelLimits{}
	if strings.TrimSpace(raw) == "" {
		return limits, nil
	}
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&limits); err != nil || limits == nil {
		return nil, fmt.Errorf("invalid AI_MODEL_LIMITS_JSON")
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return nil, fmt.Errorf("invalid AI_MODEL_LIMITS_JSON")
	}
	for key, value := range limits {
		provider, model, ok := strings.Cut(key, "/")
		if !ok || !llm.ValidateSelection([]string{provider}, provider, model) || !value.Valid() {
			return nil, fmt.Errorf("invalid AI_MODEL_LIMITS_JSON")
		}
	}
	return limits, nil
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
	if cfg.AIEnabled {
		if strings.TrimSpace(cfg.AICredentialKeys) == "" || cfg.AICredentialActiveKeyVersion == "" {
			return fmt.Errorf("AI_ENABLED requires AI_CREDENTIAL_KEYS and AI_CREDENTIAL_ACTIVE_KEY_VERSION")
		}
		if _, err := secret.Parse(cfg.AICredentialKeys, cfg.AICredentialActiveKeyVersion); err != nil {
			return err
		}
		if len(cfg.AIEnabledProviders) == 0 {
			return fmt.Errorf("AI_ENABLED requires at least one AI_ENABLED_PROVIDERS entry")
		}
		seen := map[string]bool{}
		for _, provider := range cfg.AIEnabledProviders {
			if seen[provider] || (provider != "glm" && provider != "qwen" && provider != "deepseek" && provider != "openai" && provider != "kimi") {
				return fmt.Errorf("invalid AI_ENABLED_PROVIDERS")
			}
			seen[provider] = true
		}
		if cfg.AIWorkers < 1 || cfg.AIWorkers > 16 || cfg.AIQueueCapacity < 1 || cfg.AIQueueCapacity > 4096 {
			return fmt.Errorf("invalid AI capacity configuration")
		}
	}

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
	if cfg.PublicBaseURL != "" {
		u, err := url.Parse(cfg.PublicBaseURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" {
			return fmt.Errorf("invalid APP_PUBLIC_URL: use an http(s) origin without credentials, path, query or fragment")
		}
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
	if cfg.AuthSessionTTL < time.Hour || cfg.AuthSessionTTL > 30*24*time.Hour || cfg.AuthSessionTTL < cfg.JWTTTL {
		return fmt.Errorf("invalid AUTH_SESSION_TTL: must be between 1h and 720h and at least JWT_TTL")
	}
	if strings.TrimSpace(cfg.JWTIssuer) == "" {
		return fmt.Errorf("invalid JWT_ISSUER: must not be blank")
	}
	if cfg.CollectorLockTTL < 3*time.Second || cfg.ArXivBootstrapLookback <= 0 ||
		cfg.ArXivRecoveryOverlap <= 0 || cfg.ArXivSyncRetryInterval <= 0 {
		return fmt.Errorf("invalid collector scheduling configuration")
	}
	if _, err := time.Parse("15:04", cfg.ArXivDailySyncTime); err != nil {
		return fmt.Errorf("invalid ARXIV_DAILY_SYNC_TIME: must use HH:mm")
	}
	feedURL, err := url.Parse(cfg.ArXivFeedEndpoint)
	if err != nil || feedURL.Scheme != "https" || !strings.EqualFold(feedURL.Hostname(), "rss.arxiv.org") {
		return fmt.Errorf("invalid ARXIV_FEED_ENDPOINT")
	}
	if cfg.ArXivPageSize < 1 || cfg.ArXivPageSize > 2000 || cfg.ArXivMaxPages < 1 ||
		cfg.ArXivMaxResponseBytes < 1024 || cfg.ArXivRequestAttempts < 1 ||
		cfg.ArXivRequestBackoff < 0 || cfg.ArXivRequestInterval < 3*time.Second ||
		cfg.ArXivHTTPTimeout <= 0 {
		return fmt.Errorf("invalid arxiv configuration")
	}
	if cfg.MatcherWorkers < 1 || cfg.MatcherQueueCapacity < 1 {
		return fmt.Errorf("invalid matcher configuration")
	}
	if cfg.OpsStatusRetention < time.Hour {
		return fmt.Errorf("invalid OPS_STATUS_RETENTION: must be at least 1h")
	}
	if cfg.DigestInterval <= 0 ||
		cfg.MailWorkers < 1 || cfg.MailQueueCapacity < 1 || cfg.SMTPTimeout <= 0 {
		return fmt.Errorf("invalid digest configuration")
	}
	if _, _, err := net.SplitHostPort(cfg.SMTPAddr); err != nil {
		return fmt.Errorf("invalid SMTP_ADDR: must include host and port")
	}
	if _, err := mail.ParseAddress(cfg.SMTPFrom); err != nil {
		return fmt.Errorf("invalid SMTP_FROM")
	}
	if (cfg.SMTPUsername == "") != (cfg.SMTPPassword == "") {
		return fmt.Errorf("invalid SMTP authentication configuration")
	}

	return nil
}

func splitCSV(raw string) []string {
	result := make([]string, 0)
	for _, value := range strings.Split(raw, ",") {
		if value = strings.ToLower(strings.TrimSpace(value)); value != "" {
			result = append(result, value)
		}
	}
	return result
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

func durationEnvOrDefault(key string, fallback time.Duration) time.Duration {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	value, err := time.ParseDuration(raw)
	if err != nil {
		return 0
	}
	return value
}

func intEnvOrDefault(key string, fallback int) int {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return 0
	}
	return value
}

func int64EnvOrDefault(key string, fallback int64) int64 {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0
	}
	return value
}

func stringEnvOrDefault(key, fallback string) string {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	return value
}

func boolEnvOrDefault(key string, fallback bool) (bool, error) {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.ParseBool(raw)
	if err != nil {
		return false, fmt.Errorf("invalid %s: must be a boolean", key)
	}
	return value, nil
}
