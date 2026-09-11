package config

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestLoadReadsJWTConfiguration(t *testing.T) {
	setValidEnvironment(t)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.JWTSecret != developmentJWTSecret {
		t.Fatal("JWT secret was not loaded")
	}
	if cfg.JWTTTL != 15*time.Minute {
		t.Fatalf("expected JWT TTL 15m, got %s", cfg.JWTTTL)
	}
	if cfg.JWTIssuer != "signalwatch-api" {
		t.Fatalf("expected JWT issuer signalwatch-api, got %q", cfg.JWTIssuer)
	}
	if cfg.CollectorLockTTL != 55*time.Minute || cfg.ArXivBootstrapLookback != 7*24*time.Hour ||
		cfg.ArXivRecoveryOverlap != 24*time.Hour || cfg.ArXivDailySyncTime != "00:30" ||
		cfg.ArXivSyncRetryInterval != 15*time.Minute ||
		cfg.ArXivFeedEndpoint != "https://rss.arxiv.org/atom" {
		t.Fatalf("unexpected V2 collector defaults: %+v", cfg)
	}
	if cfg.MatcherWorkers != 4 || cfg.MatcherQueueCapacity != 256 {
		t.Fatalf("unexpected matcher defaults: workers=%d queue=%d",
			cfg.MatcherWorkers, cfg.MatcherQueueCapacity)
	}
	if cfg.OpsStatusRetention != 168*time.Hour {
		t.Fatalf("unexpected operations status retention: %s", cfg.OpsStatusRetention)
	}
	if cfg.DigestInterval != time.Minute || cfg.MailWorkers != 2 ||
		cfg.MailQueueCapacity != 128 || cfg.SMTPAddr != "127.0.0.1:1025" ||
		cfg.SMTPFrom != "SignalWatch <digest@signalwatch.local>" || cfg.SMTPStartTLS ||
		cfg.SMTPTimeout != 10*time.Second {
		t.Fatalf("unexpected M4 defaults: %+v", cfg)
	}
}

func TestLoadRequiresJWTConfiguration(t *testing.T) {
	for _, name := range []string{envJWTSecret, envJWTTTL, envJWTIssuer} {
		t.Run(name, func(t *testing.T) {
			setValidEnvironment(t)
			if err := os.Unsetenv(name); err != nil {
				t.Fatalf("unset %s: %v", name, err)
			}

			_, err := Load()
			if err == nil || !strings.Contains(err.Error(), name) {
				t.Fatalf("expected missing %s error, got %v", name, err)
			}
		})
	}
}

func TestLoadRejectsInvalidJWTTTLWithoutEchoingSecret(t *testing.T) {
	setValidEnvironment(t)
	const secret = "SECRET_VALUE_MUST_NEVER_APPEAR_123456789"
	t.Setenv(envJWTSecret, secret)
	t.Setenv(envJWTTTL, "not-a-duration")

	_, err := Load()
	if err == nil {
		t.Fatal("expected invalid JWT TTL error")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatal("configuration error leaked JWT secret")
	}
}

func TestValidateRejectsUnsafeJWTConfiguration(t *testing.T) {
	valid := validConfig()
	tests := []struct {
		name   string
		mutate func(*Config)
	}{
		{
			name: "short secret",
			mutate: func(cfg *Config) {
				cfg.JWTSecret = "too-short"
			},
		},
		{
			name: "development secret in production",
			mutate: func(cfg *Config) {
				cfg.AppEnv = "production"
				cfg.JWTSecret = developmentJWTSecret
			},
		},
		{
			name: "zero TTL",
			mutate: func(cfg *Config) {
				cfg.JWTTTL = 0
			},
		},
		{
			name: "TTL above 24 hours",
			mutate: func(cfg *Config) {
				cfg.JWTTTL = 24*time.Hour + time.Nanosecond
			},
		},
		{
			name: "blank issuer",
			mutate: func(cfg *Config) {
				cfg.JWTIssuer = "   "
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := valid
			test.mutate(&cfg)
			if err := validate(cfg); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestLoadRejectsInvalidCollectorConfiguration(t *testing.T) {
	tests := []struct {
		name  string
		key   string
		value string
	}{
		{name: "arxiv interval below three seconds", key: envArXivRequestInterval, value: "2s"},
		{name: "collector lock is not positive", key: envCollectorLockTTL, value: "0s"},
		{name: "bootstrap lookback is not positive", key: envArXivBootstrapLookback, value: "0s"},
		{name: "recovery overlap is not positive", key: envArXivRecoveryOverlap, value: "0s"},
		{name: "retry interval is not positive", key: envArXivSyncRetryInterval, value: "0s"},
		{name: "daily time is invalid", key: envArXivDailySyncTime, value: "25:00"},
		{name: "feed endpoint is invalid", key: envArXivFeedEndpoint, value: "https://example.com/atom"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			setValidEnvironment(t)
			t.Setenv(test.key, test.value)
			if _, err := Load(); err == nil {
				t.Fatal("expected M2 configuration validation error")
			}
		})
	}
}

func TestLoadRejectsInvalidMatcherConfiguration(t *testing.T) {
	for _, test := range []struct {
		name  string
		key   string
		value string
	}{
		{name: "worker count is not positive", key: envMatcherWorkers, value: "0"},
		{name: "queue capacity is not positive", key: envMatcherQueueCapacity, value: "0"},
		{name: "worker count is not an integer", key: envMatcherWorkers, value: "many"},
	} {
		t.Run(test.name, func(t *testing.T) {
			setValidEnvironment(t)
			t.Setenv(test.key, test.value)
			if _, err := Load(); err == nil {
				t.Fatal("expected M3 matcher configuration validation error")
			}
		})
	}
}

func TestLoadRejectsShortOperationsStatusRetention(t *testing.T) {
	setValidEnvironment(t)
	t.Setenv(envOpsStatusRetention, "59m")
	if _, err := Load(); err == nil {
		t.Fatal("expected operations status retention validation error")
	}
}

func TestLoadRejectsInvalidDigestConfiguration(t *testing.T) {
	for _, test := range []struct {
		name  string
		key   string
		value string
	}{
		{name: "scheduler interval", key: envDigestInterval, value: "0s"},
		{name: "mail workers", key: envMailWorkers, value: "0"},
		{name: "queue capacity", key: envMailQueueCapacity, value: "many"},
		{name: "SMTP address", key: envSMTPAddr, value: "missing-port"},
		{name: "SMTP sender", key: envSMTPFrom, value: "not-an-address"},
		{name: "STARTTLS", key: envSMTPStartTLS, value: "sometimes"},
		{name: "SMTP timeout", key: envSMTPTimeout, value: "0s"},
	} {
		t.Run(test.name, func(t *testing.T) {
			setValidEnvironment(t)
			t.Setenv(test.key, test.value)
			if _, err := Load(); err == nil {
				t.Fatal("expected M4 configuration validation error")
			}
		})
	}
}

func setValidEnvironment(t *testing.T) {
	t.Helper()
	values := map[string]string{
		envAppEnv:            "test",
		envHTTPAddr:          "127.0.0.1:8080",
		envLogLevel:          "info",
		envWorkerHeartbeat:   "10s",
		envMySQLDSN:          "user:password@tcp(127.0.0.1:3306)/signalwatch",
		envMySQLMaxOpenConns: "10",
		envMySQLMaxIdleConns: "5",
		envRedisAddr:         "127.0.0.1:6379",
		envRedisPassword:     "password",
		envRedisDB:           "0",
		envJWTSecret:         developmentJWTSecret,
		envJWTTTL:            "15m",
		envJWTIssuer:         "signalwatch-api",
	}
	for name, value := range values {
		t.Setenv(name, value)
	}
}

func validConfig() Config {
	return Config{
		AppEnv:                 "test",
		HTTPAddr:               "127.0.0.1:8080",
		LogLevel:               "info",
		WorkerHeartbeat:        10 * time.Second,
		MySQLMaxOpenConns:      10,
		MySQLMaxIdleConns:      5,
		RedisDB:                0,
		JWTSecret:              "a-strong-random-production-secret-123",
		JWTTTL:                 15 * time.Minute,
		AuthSessionTTL:         168 * time.Hour,
		JWTIssuer:              "signalwatch-api",
		CollectorLockTTL:       55 * time.Minute,
		ArXivBootstrapLookback: 7 * 24 * time.Hour,
		ArXivRecoveryOverlap:   24 * time.Hour,
		ArXivDailySyncTime:     "00:30",
		ArXivSyncRetryInterval: 15 * time.Minute,
		ArXivFeedEndpoint:      "https://rss.arxiv.org/atom",
		ArXivPageSize:          100,
		ArXivMaxPages:          10,
		ArXivMaxResponseBytes:  5 << 20,
		ArXivRequestAttempts:   3,
		ArXivRequestBackoff:    5 * time.Second,
		ArXivRequestInterval:   3 * time.Second,
		ArXivHTTPTimeout:       30 * time.Second,
		MatcherWorkers:         4,
		MatcherQueueCapacity:   256,
		OpsStatusRetention:     168 * time.Hour,
		DigestInterval:         time.Minute,
		MailWorkers:            2,
		MailQueueCapacity:      128,
		SMTPAddr:               "127.0.0.1:1025",
		SMTPFrom:               "SignalWatch <digest@signalwatch.local>",
		SMTPTimeout:            10 * time.Second,
	}
}

func TestOptionalAIConfiguration(t *testing.T) {
	setValidEnvironment(t)
	for _, name := range []string{"AI_CREDENTIAL_KEYS", "AI_CREDENTIAL_ACTIVE_KEY_VERSION", "AI_ENABLED_PROVIDERS", "AI_WORKERS", "AI_QUEUE_CAPACITY"} {
		t.Setenv(name, "")
	}
	t.Setenv("AI_ENABLED", "false")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AIEnabled || cfg.AIWorkers != 2 || cfg.AIQueueCapacity != 128 {
		t.Fatal("AI defaults changed")
	}
	t.Setenv("AI_ENABLED", "true")
	if _, err = Load(); err == nil {
		t.Fatal("enabled AI accepted missing configuration")
	}
	t.Setenv("AI_CREDENTIAL_KEYS", "v1:MTExMTExMTExMTExMTExMTExMTExMTExMTExMTExMTE=")
	t.Setenv("AI_CREDENTIAL_ACTIVE_KEY_VERSION", "v1")
	t.Setenv("AI_ENABLED_PROVIDERS", "glm,qwen,deepseek,kimi,openai")
	if _, err = Load(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AI_ENABLED_PROVIDERS", "unknown")
	if _, err = Load(); err == nil {
		t.Fatal("accepted unknown provider")
	}
}

func TestValidatePublicOrigin(t *testing.T) {
	for _, tc := range []struct {
		value string
		valid bool
	}{
		{"", true}, {"https://watch.example.test", true}, {"http://127.0.0.1:8080", true},
		{"javascript:alert(1)", false}, {"https://user:secret@example.test", false},
		{"https://example.test/path", false}, {"https://example.test?key=secret", false},
		{"https://example.test#fragment", false},
	} {
		cfg := validConfig()
		cfg.PublicBaseURL = tc.value
		err := validate(cfg)
		if (err == nil) != tc.valid {
			t.Errorf("public URL validity mismatch: valid=%v err=%v", tc.valid, err)
		}
	}
}

func TestBrowserSessionLifetimeConfiguration(t *testing.T) {
	setValidEnvironment(t)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AuthSessionTTL != 168*time.Hour {
		t.Fatalf("default session lifetime %s", cfg.AuthSessionTTL)
	}
	for _, value := range []string{"bad", "0s", "721h", "30m"} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("AUTH_SESSION_TTL", value)
			if _, err := Load(); err == nil {
				t.Fatalf("accepted %s", value)
			}
		})
	}
}

func TestAICallPolicyParsing(t *testing.T) {
	setValidEnvironment(t)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AIConfigTestMinInterval != 10*time.Second || cfg.AIGenerationMinInterval != 2*time.Second || cfg.AIPaperDailyLimit != 0 || cfg.AIConfigTestDailyLimit != 0 || cfg.AIDigestDailyLimit != 0 {
		t.Fatal("unexpected AI policy defaults")
	}
	for _, name := range []string{"AI_CONFIG_TEST_DAILY_LIMIT", "AI_PAPER_DAILY_LIMIT", "AI_DIGEST_DAILY_LIMIT"} {
		for _, bad := range []string{"", "-1", "1.5", "1000001"} {
			t.Run(name+bad, func(t *testing.T) {
				t.Setenv(name, bad)
				if _, e := Load(); e == nil {
					t.Fatal("invalid limit accepted")
				}
			})
		}
	}
	for _, name := range []string{"AI_CONFIG_TEST_MIN_INTERVAL", "AI_GENERATION_MIN_INTERVAL"} {
		for _, bad := range []string{"", "0s", "-1s", "2h", "bad"} {
			t.Run(name+bad, func(t *testing.T) {
				t.Setenv(name, bad)
				if _, e := Load(); e == nil {
					t.Fatal("invalid interval accepted")
				}
			})
		}
	}
	t.Setenv("AI_CONFIG_TEST_DAILY_LIMIT", "12")
	t.Setenv("AI_GENERATION_MIN_INTERVAL", "4s")
	cfg, err = Load()
	if err != nil || cfg.AIConfigTestDailyLimit != 12 || cfg.AIGenerationMinInterval != 4*time.Second {
		t.Fatalf("policy override failed: %v", err)
	}
}
