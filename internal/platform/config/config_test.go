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
		AppEnv:            "test",
		HTTPAddr:          "127.0.0.1:8080",
		LogLevel:          "info",
		WorkerHeartbeat:   10 * time.Second,
		MySQLMaxOpenConns: 10,
		MySQLMaxIdleConns: 5,
		RedisDB:           0,
		JWTSecret:         "a-strong-random-production-secret-123",
		JWTTTL:            15 * time.Minute,
		JWTIssuer:         "signalwatch-api",
	}
}
