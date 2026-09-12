// Package bootstrap is the composition root for infrastructure and use cases.
package bootstrap

import (
	"log/slog"
	"signalwatch/internal/ai"
	"signalwatch/internal/digest"
	"signalwatch/internal/paper"
	"signalwatch/internal/platform/config"
	"signalwatch/internal/platform/db"
	"signalwatch/internal/platform/llm"
	"signalwatch/internal/platform/secret"
	"signalwatch/internal/user"
	"time"
)

func OpenAI(cfg config.Config, logger *slog.Logger, completion digest.CompletionReader) (*ai.Service, func(), error) {
	if !cfg.AIEnabled {
		return ai.Disabled(logger), func() {}, nil
	}
	pool := cfg
	pool.MySQLMaxOpenConns = 4
	pool.MySQLMaxIdleConns = 2
	database, err := db.Open(pool)
	if err != nil {
		return nil, nil, err
	}
	sqlDB, err := database.DB()
	if err != nil {
		return nil, nil, err
	}
	close := func() { _ = sqlDB.Close() }
	keyring, err := secret.Parse(cfg.AICredentialKeys, cfg.AICredentialActiveKeyVersion)
	if err != nil {
		close()
		return nil, nil, err
	}
	calls := ai.NewCallRunner(ai.NewMySQLCallStore(database), ai.CallPolicy{ConfigInterval: cfg.AIConfigTestMinInterval, GenerationInterval: cfg.AIGenerationMinInterval, ConfigDailyLimit: cfg.AIConfigTestDailyLimit, PaperDailyLimit: cfg.AIPaperDailyLimit, DigestDailyLimit: cfg.AIDigestDailyLimit, SubscriptionAgentDailyLimit: cfg.AISubscriptionAgentDailyLimit, PaperQADailyLimit: cfg.AIPaperQADailyLimit}, func(e []string, p, m, k string) (ai.Generator, error) { return llm.NewProviderClient(e, p, m, k) }, cfg.AIEnabledProviders, time.Now, logger)
	configurations := ai.NewConfigurationService(ai.NewMySQLConfigurationStore(database), keyring, cfg.AIEnabledProviders, time.Now, calls, llm.Catalog{})
	service := ai.NewService(ai.Dependencies{Tasks: &ai.Repository{DB: database}, Configurations: configurations, Calls: calls, Users: user.NewRepository(database), Papers: paper.NewQueryService(paper.NewQueryRepository(database)), Digests: digest.NewRepository(database), Completion: completion}, cfg.AIWorkers, cfg.AIQueueCapacity, cfg.AIEnabledProviders, logger, time.Now)
	return service, close, nil
}
