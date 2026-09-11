package ai

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	driver "github.com/go-sql-driver/mysql"
	"gorm.io/gorm"

	"signalwatch/internal/digest"
	"signalwatch/internal/insight"
	"signalwatch/internal/paper"
	"signalwatch/internal/platform/config"
	platformdb "signalwatch/internal/platform/db"
	"signalwatch/internal/platform/llm"
	"signalwatch/internal/platform/secret"
	"signalwatch/internal/source"
	"signalwatch/internal/subscription"
	"signalwatch/internal/user"
)

type GeneratorFunc func(context.Context, string, []byte, int) (llm.Result, error)

func (f GeneratorFunc) GenerateLimit(c context.Context, p string, b []byte, n int) (llm.Result, error) {
	return f(c, p, b, n)
}

type aiIntegrationFixture struct {
	db             *gorm.DB
	now            time.Time
	users          []user.User
	papers         []paper.Paper
	subscriptions  []subscription.Subscription
	service        *Service
	configurations *ConfigurationService
	calls          atomic.Int64
}

func newAIIntegrationFixture(t *testing.T) *aiIntegrationFixture {
	t.Helper()
	dsn := os.Getenv("M1_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("requires migrated dedicated M1_TEST_MYSQL_DSN")
	}
	parsed, err := driver.ParseDSN(dsn)
	if err != nil || !strings.HasSuffix(parsed.DBName, "_test") {
		t.Fatal("requires isolated _test database")
	}
	database, err := platformdb.Open(config.Config{MySQLDSN: dsn, MySQLMaxOpenConns: 12, MySQLMaxIdleConns: 4})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := database.DB()
	t.Cleanup(func() { _ = sqlDB.Close() })
	f := &aiIntegrationFixture{db: database, now: time.Date(2036, 9, 9, 8, 0, 0, 0, time.UTC)}
	nonce := fmt.Sprint(time.Now().UnixNano())
	src := source.Source{SourceKey: "byok-" + nonce, Kind: source.KindArXiv, Name: "BYOK fixture", Enabled: false, ConfigJSON: json.RawMessage(`{}`), CreatedAt: f.now, UpdatedAt: f.now}
	mustAI(t, database.Create(&src).Error)
	t.Cleanup(func() {
		for _, u := range f.users {
			database.Unscoped().Where("user_id=?", u.ID).Delete(&subscription.Subscription{})
		}
		for _, u := range f.users {
			database.Unscoped().Delete(&u)
		}
		database.Where("source_id=?", src.ID).Delete(&paper.Paper{})
		database.Unscoped().Delete(&src)
	})
	for i := 0; i < 2; i++ {
		u := user.User{Email: fmt.Sprintf("byok-%s-%d@example.test", nonce, i), PasswordHash: "unused", Timezone: "UTC", DigestTime: "08:20:00", MaxItemsPerDigest: 20, Status: user.StatusActive, Role: user.RoleUser, AIEnabled: true, AILanguage: "zh", CreatedAt: f.now, UpdatedAt: f.now}
		mustAI(t, database.Create(&u).Error)
		f.users = append(f.users, u)
	}
	for i := 0; i < 3; i++ {
		p := paper.Paper{SourceID: src.ID, ArXivID: fmt.Sprintf("byok-%s-%d", nonce, i), Title: fmt.Sprintf("VLM adaptation %d", i), Abstract: "We study visual language model adaptation.", AuthorsJSON: json.RawMessage(`[]`), CategoriesJSON: json.RawMessage(`["cs.AI"]`), PublishedAt: f.now, ArXivUpdatedAt: f.now, FirstSeenAt: f.now, CreatedAt: f.now, UpdatedAt: f.now, ArXivURL: "https://arxiv.org/abs/fixture"}
		mustAI(t, database.Create(&p).Error)
		f.papers = append(f.papers, p)
	}
	for _, u := range f.users {
		sub := subscription.Subscription{UserID: u.ID, SourceID: src.ID, Name: "VLM", Category: "cs.AI", KeywordsJSON: json.RawMessage(`[]`), Enabled: true, Version: 1, MaxItemsPerDigest: 20, DigestAIEnabled: true, DigestAILanguage: "zh", CreatedAt: f.now, UpdatedAt: f.now}
		mustAI(t, database.Create(&sub).Error)
		f.subscriptions = append(f.subscriptions, sub)
		for _, p := range f.papers {
			mustAI(t, database.Exec("INSERT INTO subscription_papers(subscription_id,paper_id,matched_keywords_json,matched_at) VALUES(?,?, '[]',?)", sub.ID, p.ID, f.now).Error)
		}
	}
	master := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))
	keyring, err := secret.Parse("v1:"+master, "v1")
	mustAI(t, err)
	enabledProviders := []string{"glm", "qwen", "deepseek", "kimi", "openai"}
	factory := func(_ []string, _, _, _ string) (Generator, error) {
		return GeneratorFunc(func(_ context.Context, _ string, raw []byte, max int) (llm.Result, error) {
			f.calls.Add(1)
			if max == 1024 {
				return llm.Result{Content: []byte(`{"ok":true}`), InputTokens: 2, OutputTokens: 1, UsageKnown: true}, nil
			}
			var request struct {
				Language string          `json:"language"`
				Papers   []insight.Paper `json:"papers"`
			}
			if err := json.Unmarshal(raw, &request); err != nil {
				return llm.Result{}, err
			}
			var payload any
			if len(request.Papers) == 1 {
				payload = insight.Summary{Summary: "Supported summary", Contributions: []string{"Adaptation"}, Method: "Not specified in abstract", Applications: []insight.Application{}, Evidence: []insight.Evidence{{Field: "abstract", Quote: request.Papers[0].Abstract}}, Limitations: "Title and abstract only"}
			} else {
				payload = insight.Overview{Summary: "This selection studies VLM adaptation.", Themes: []insight.Theme{{Title: "Adaptation", Description: "Shared topic", PaperIDs: []uint64{request.Papers[0].ID, request.Papers[1].ID}}}}
			}
			content, _ := json.Marshal(payload)
			return llm.Result{Content: content, InputTokens: 10, OutputTokens: 5, UsageKnown: true}, nil
		}), nil
	}
	f.configurations = NewConfigurationService(NewMySQLConfigurationStore(database), keyring, enabledProviders, func() time.Time { return f.now }, NewCallRunner(NewMySQLCallStore(database), CallPolicy{}, factory, enabledProviders, func() time.Time { return f.now }, slog.New(slog.NewTextHandler(io.Discard, nil))), llm.Catalog{})
	for _, u := range f.users {
		insertAIConfiguration(t, f, u.ID, "test-api-key-1234")
	}
	f.service = NewService(Dependencies{Tasks: &Repository{DB: database}, Configurations: f.configurations, Calls: f.configurations.calls, Users: user.NewRepository(database), Papers: paper.NewQueryService(paper.NewQueryRepository(database)), Digests: digest.NewRepository(database)}, 2, 128, enabledProviders, slog.New(slog.NewTextHandler(io.Discard, nil)), func() time.Time { return f.now })

	return f
}

func TestMySQLBYOKConfigurationReplacementIsAtomicAndVersioned(t *testing.T) {
	f := newAIIntegrationFixture(t)
	ctx := t.Context()
	userID := f.users[0].ID
	successFactory := f.configurations.calls.factory
	f.configurations.calls.factory = func(_ []string, _, _, _ string) (Generator, error) {
		return GeneratorFunc(func(context.Context, string, []byte, int) (llm.Result, error) {
			return llm.Result{}, &llm.Failure{Code: "provider_http_503", Retryable: true}
		}), nil
	}
	version := uint64(1)
	if _, err := f.configurations.Put(ctx, userID, "glm", "glm-5.2", "replacement-key-5678", &version); err == nil {
		t.Fatal("failed provider test replaced configuration")
	}
	current, original, err := f.configurations.loadCurrent(ctx, userID)
	mustAI(t, err)
	if current.ConfigVersion != 1 || current.ModelID != "glm-4.7-flash" || string(original) != "test-api-key-1234" {
		t.Fatalf("old configuration was not preserved: %+v %q", current, original)
	}
	clear(original)

	f.configurations.calls.factory = successFactory
	updated, err := f.configurations.Put(ctx, userID, "glm", "glm-5.2", "replacement-key-5678", &version)
	mustAI(t, err)
	if updated.Version != 2 || updated.ModelID != "glm-5.2" || updated.MaskedKey != "••••5678" {
		t.Fatalf("replacement=%+v", updated)
	}
	usable, err := f.configurations.ForUse(ctx, userID, 2)
	mustAI(t, err)
	if string(usable.Secret) != "replacement-key-5678" {
		t.Fatal("replacement secret cannot be decrypted")
	}
	clear(usable.Secret)
	if _, err := f.configurations.ForUse(ctx, userID, 1); !errors.Is(err, ErrConfigurationRequired) {
		t.Fatalf("old configuration version remained usable: %v", err)
	}
	if _, err := f.configurations.ChangeModel(ctx, userID, 1, "glm-4.7-flash"); !errors.Is(err, ErrConfigurationConflict) {
		t.Fatalf("stale If-Match accepted: %v", err)
	}
	rotated, err := f.configurations.RotateSecret(ctx, userID, 2, "rotated-api-key-9999")
	mustAI(t, err)
	if rotated.Version != 3 || rotated.MaskedKey != "••••9999" {
		t.Fatalf("rotation=%+v", rotated)
	}
}

func TestMySQLRetiredModelCanSwitchUsingSavedKey(t *testing.T) {
	f := newAIIntegrationFixture(t)
	ctx := t.Context()
	userID := f.users[0].ID
	row, key, err := f.configurations.loadCurrent(ctx, userID)
	mustAI(t, err)
	defer clear(key)
	row.ModelID = "glm-4.3"
	row.SecretCiphertext, row.SecretNonce, row.MasterKeyVersion, err = f.configurations.keyring.Encrypt(key, secret.AAD(userID, row.ProviderID, row.ModelID, row.ConfigVersion))
	mustAI(t, err)
	mustAI(t, f.db.Save(&row).Error)

	public, err := f.configurations.Get(ctx, userID)
	mustAI(t, err)
	if public.Usable || public.UnusableReason != "model_retired" {
		t.Fatalf("retired configuration=%+v", public)
	}
	active, err := f.configurations.Active(ctx, userID)
	mustAI(t, err)
	if active {
		t.Fatal("retired configuration remained active")
	}

	updated, err := f.configurations.ChangeModel(ctx, userID, row.ConfigVersion, "glm-5.2")
	mustAI(t, err)
	if !updated.Usable || updated.ModelID != "glm-5.2" || updated.Version != row.ConfigVersion+1 {
		t.Fatalf("updated configuration=%+v", updated)
	}
}

func TestMySQLQwenAndKimiConfigurationLifecycle(t *testing.T) {
	f := newAIIntegrationFixture(t)
	ctx := t.Context()
	userID := f.users[0].ID

	mustAI(t, f.configurations.Delete(ctx, userID, 1))
	qwen, err := f.configurations.Put(ctx, userID, "qwen", "qwen3.8-flash", "qwen-api-key-1234", nil)
	mustAI(t, err)
	if !qwen.Usable || qwen.ProviderID != "qwen" || qwen.ModelID != "qwen3.8-flash" || qwen.Version != 2 {
		t.Fatalf("qwen configuration=%+v", qwen)
	}
	qwen, err = f.configurations.ChangeModel(ctx, userID, qwen.Version, "qwen3.8-max")
	mustAI(t, err)
	if !qwen.Usable || qwen.ModelID != "qwen3.8-max" || qwen.Version != 3 {
		t.Fatalf("qwen model switch=%+v", qwen)
	}
	kimi, err := f.configurations.Put(ctx, userID, "kimi", "kimi-k2.6", "kimi-api-key-5678", &qwen.Version)
	mustAI(t, err)
	if !kimi.Usable || kimi.ProviderID != "kimi" || kimi.ModelID != "kimi-k2.6" || kimi.Version != 4 {
		t.Fatalf("kimi replacement=%+v", kimi)
	}
}

func insertAIConfiguration(t *testing.T, f *aiIntegrationFixture, userID uint64, key string) {
	t.Helper()
	version := uint64(1)
	ciphertext, nonce, master, err := f.configurations.keyring.Encrypt([]byte(key), secret.AAD(userID, "glm", "glm-4.7-flash", version))
	mustAI(t, err)
	row := Configuration{Generation: "fixture", UserID: userID, ProviderID: "glm", ModelID: "glm-4.7-flash", Status: ConfigurationActive, ConfigVersion: version, KeyHint: key[len(key)-4:], SecretCiphertext: ciphertext, SecretNonce: nonce, MasterKeyVersion: master, CreatedAt: f.now, UpdatedAt: f.now}
	mustAI(t, f.db.Create(&row).Error)
	mustAI(t, f.db.Exec("INSERT INTO ai_configuration_counters(user_id,revision) VALUES (?,1)", userID).Error)
}
func mustAI(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func TestMySQLBYOKPaperCacheIsUserScopedAndManualRetry(t *testing.T) {
	f := newAIIntegrationFixture(t)
	ctx := t.Context()
	for _, u := range f.users {
		response, err := f.service.Summary(ctx, u.ID, f.papers[0].ID, "zh", true)
		mustAI(t, err)
		if response.Provider != "glm" || response.Model != "glm-4.7-flash" || response.Items[0].State != "pending" {
			t.Fatalf("response=%+v", response)
		}
	}
	rows, err := f.service.repo.Pending(ctx, PaperKind, "", f.now, 10)
	mustAI(t, err)
	if len(rows) != 2 {
		t.Fatalf("user-scoped rows=%d", len(rows))
	}
	for _, task := range rows {
		f.service.Process(ctx, task)
	}
	if f.calls.Load() != 2 {
		t.Fatalf("calls=%d", f.calls.Load())
	}
	for _, u := range f.users {
		response, err := f.service.Summary(ctx, u.ID, f.papers[0].ID, "zh", false)
		mustAI(t, err)
		if response.Items[0].Content == nil {
			t.Fatalf("missing cache for user %d", u.ID)
		}
	}

	// A failed provider call is terminal, and only an explicit detail-page
	// request revives it for one more paid call.
	f.configurations.calls.factory = func(_ []string, _, _, _ string) (Generator, error) {
		return GeneratorFunc(func(context.Context, string, []byte, int) (llm.Result, error) {
			f.calls.Add(1)
			return llm.Result{}, &llm.Failure{Code: "provider_http_429", Retryable: true}
		}), nil
	}
	_, err = f.service.Summary(ctx, f.users[0].ID, f.papers[1].ID, "zh", true)
	mustAI(t, err)
	rows, err = f.service.repo.Pending(ctx, PaperKind, "", f.now, 10)
	mustAI(t, err)
	f.service.Process(ctx, rows[0])
	key := newUserTask(PaperKind, f.users[0].ID, f.papers[1].ID, "", "zh", "glm", "glm-4.7-flash", "fixture", 1, generationProfile("glm", "glm-4.7-flash"), Input{Papers: []insight.Paper{{ID: f.papers[1].ID, Title: f.papers[1].Title, Abstract: f.papers[1].Abstract}}}, 1, f.now)
	failed, err := f.service.repo.Find(ctx, key)
	mustAI(t, err)
	if failed.Status != "failed" || failed.Attempts != 1 {
		t.Fatalf("failed task=%+v", failed)
	}
	_, err = f.service.Summary(ctx, f.users[0].ID, f.papers[1].ID, "zh", true)
	mustAI(t, err)
	pending, err := f.service.repo.Find(ctx, key)
	mustAI(t, err)
	if pending.Status != "pending" || pending.Attempts != 0 {
		t.Fatalf("manual retry=%+v", pending)
	}
}

func TestMySQLBYOKDigestOncePerSubscriptionAndExactFingerprint(t *testing.T) {
	f := newAIIntegrationFixture(t)
	ctx := t.Context()
	f.service.scanner.prepareDigest(ctx, f.users[0], f.now)
	f.service.scanner.prepareDigest(ctx, f.users[0], f.now)
	rows, err := f.service.repo.Pending(ctx, DigestKind, "", f.now, 10)
	mustAI(t, err)
	if len(rows) != 1 {
		t.Fatalf("digest tasks=%d", len(rows))
	}
	f.service.Process(ctx, rows[0])
	u, err := f.service.executor.digest.FindActiveUser(ctx, f.users[0].ID, f.subscriptions[0].ID)
	mustAI(t, err)
	items, err := f.service.executor.digest.ListCandidates(ctx, u.ID, u.SubscriptionID, 20)
	mustAI(t, err)
	result, err := f.service.Lookup(ctx, u, digest.Job{UserID: u.ID, SubscriptionID: u.SubscriptionID, LocalDate: "2036-09-09"}, items)
	mustAI(t, err)
	if len(result.Digests) != 1 {
		t.Fatalf("digest=%+v", result)
	}
	result, err = f.service.Lookup(ctx, u, digest.Job{UserID: u.ID, SubscriptionID: u.SubscriptionID, LocalDate: "2036-09-09"}, items[:2])
	mustAI(t, err)
	if len(result.Digests) != 0 {
		t.Fatal("stale candidate fingerprint reused")
	}
}

func TestMySQLBYOKUsageLeaseAndDeleteConfiguration(t *testing.T) {
	f := newAIIntegrationFixture(t)
	ctx := t.Context()
	mustAI(t, f.configurations.Delete(ctx, f.users[0].ID, 1))
	var count int64
	mustAI(t, f.db.Model(&Configuration{}).Where("user_id=?", f.users[0].ID).Count(&count).Error)
	if count != 0 {
		t.Fatal("configuration retained")
	}
	var state struct {
		AIEnabled       bool
		DigestAIEnabled bool
	}
	mustAI(t, f.db.Table("users u").Select("u.ai_enabled,s.digest_ai_enabled").Joins("JOIN subscriptions s ON s.user_id=u.id").Where("u.id=?", f.users[0].ID).Scan(&state).Error)
	if state.AIEnabled || state.DigestAIEnabled {
		t.Fatalf("AI switches retained: %+v", state)
	}
}

func TestUpcomingDate(t *testing.T) {
	now := time.Date(2026, 9, 9, 15, 45, 0, 0, time.UTC)
	if date, ok := UpcomingDate(now, "Asia/Shanghai", "00:10:00"); !ok || date != "2026-09-10" {
		t.Fatalf("%s %v", date, ok)
	}
	if _, ok := UpcomingDate(now, "invalid", "00:10:00"); ok {
		t.Fatal("invalid timezone")
	}
}
