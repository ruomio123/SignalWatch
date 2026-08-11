package config

import (
	"fmt"
	"net"
	"os"
	"time"
)

type Config struct {
	AppEnv          string
	HTTPAddr        string
	LogLevel        string
	WorkerHeartbeat time.Duration
}

const (
	envAppEnv          = "APP_ENV"
	envHTTPAddr        = "HTTP_ADDR"
	envLogLevel        = "LOG_LEVEL"
	envWorkerHeartbeat = "WORKER_HEARTBEAT"
)

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

	cfg := Config{
		AppEnv:          appEnv,
		HTTPAddr:        httpAddr,
		LogLevel:        logLevel,
		WorkerHeartbeat: heartbeat,
	}

	if err := validate(cfg); err != nil {
		return Config{}, err
	}

	return cfg, nil
}

func requiredEnv(key string) (string, error) {
	value, ok := os.LookupEnv(key)
	if !ok || value == "" {
		return "", fmt.Errorf("missing required env: %s", key)
	}
	return value, nil
}

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
		return fmt.Errorf("invalid LOG_ENV: %s", cfg.LogLevel)
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
	return nil
}
