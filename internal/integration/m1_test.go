package integration_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	mysqldriver "github.com/go-sql-driver/mysql"
	"gorm.io/gorm"

	"signalwatch/internal/auth"
	"signalwatch/internal/platform/config"
	platformdb "signalwatch/internal/platform/db"
	"signalwatch/internal/platform/httpx"
	"signalwatch/internal/server"
	"signalwatch/internal/source"
	"signalwatch/internal/subscription"
	"signalwatch/internal/user"
)

const (
	testDSNEnv   = "M1_TEST_MYSQL_DSN"
	testPassword = "M1-integration-password"
)

type testAPI struct {
	router http.Handler
}

type testResponse struct {
	status    int
	requestID string
	etag      string
	body      []byte
}

type loginResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	ExpiresIn   int64  `json:"expires_in"`
}

type userResponse struct {
	ID                uint64 `json:"id"`
	Email             string `json:"email"`
	Timezone          string `json:"timezone"`
	DigestTime        string `json:"digest_time"`
	MaxItemsPerDigest uint16 `json:"max_items_per_digest"`
}

type sourceResponse struct {
	ID                uint64   `json:"id"`
	SourceKey         string   `json:"source_key"`
	Kind              string   `json:"kind"`
	Name              string   `json:"name"`
	RuleTypes         []string `json:"rule_types"`
	AllowedCategories []string `json:"allowed_categories"`
}

type rulesResponse struct {
	Categories      []string `json:"categories"`
	Authors         []string `json:"authors"`
	IncludeKeywords []string `json:"include_keywords"`
	ExcludeKeywords []string `json:"exclude_keywords"`
}

type subscriptionResponse struct {
	ID        uint64         `json:"id"`
	Name      string         `json:"name"`
	Objective *string        `json:"objective"`
	Enabled   bool           `json:"enabled"`
	Version   uint32         `json:"version"`
	Rules     rulesResponse  `json:"rules"`
	Source    sourceResponse `json:"source"`
}

type subscriptionPageResponse struct {
	Items    []subscriptionResponse `json:"items"`
	Page     int                    `json:"page"`
	PageSize int                    `json:"page_size"`
	Total    int64                  `json:"total"`
}

type errorResponse struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	RequestID string `json:"request_id"`
}

func TestM1AcceptanceAcrossUsersAndDatabaseBoundaries(t *testing.T) {
	database, sqlDB, parsedDSN := openM1TestDatabase(t)
	t.Cleanup(func() {
		if err := sqlDB.Close(); err != nil {
			t.Errorf("close isolated M1 test connection pool: %v", err)
		}
	})
	assertM1Schema(t, database)

	nonce := strconv.FormatInt(time.Now().UTC().UnixNano(), 36)
	emailA := "m1-a-" + nonce + "@example.test"
	emailB := "m1-b-" + nonce + "@example.test"
	disabledSourceKey := "disabled-" + nonce
	t.Cleanup(func() {
		cleanupM1TestData(t, database, []string{emailA, emailB}, disabledSourceKey)
	})

	var logs bytes.Buffer
	api := newM1TestAPI(t, database, sqlDB.PingContext, &logs)

	api.do(t, http.MethodGet, "/healthz", "", "", nil, http.StatusOK)
	api.do(t, http.MethodGet, "/readyz", "", "", nil, http.StatusOK)

	registeredA := decodeResponse[userResponse](t, api.do(
		t,
		http.MethodPost,
		"/api/v1/auth/register",
		"",
		"",
		map[string]any{"email": "  " + strings.ToUpper(emailA) + "  ", "password": testPassword},
		http.StatusCreated,
	))
	if registeredA.Email != emailA || registeredA.ID == 0 {
		t.Fatalf("register A: expected normalized email and positive ID")
	}
	registeredB := decodeResponse[userResponse](t, api.do(
		t,
		http.MethodPost,
		"/api/v1/auth/register",
		"",
		"",
		map[string]any{"email": emailB, "password": testPassword},
		http.StatusCreated,
	))
	if registeredB.ID == 0 || registeredB.ID == registeredA.ID {
		t.Fatal("register B: expected a distinct positive user ID")
	}

	duplicate := api.do(
		t,
		http.MethodPost,
		"/api/v1/auth/register",
		"",
		"",
		map[string]any{"email": strings.ToUpper(emailA), "password": testPassword},
		http.StatusConflict,
	)
	assertAPIError(t, duplicate, user.CodeEmailAlreadyRegistered, "email is already registered")

	missingLogin := api.do(
		t,
		http.MethodPost,
		"/api/v1/auth/login",
		"",
		"",
		map[string]any{"email": "missing-" + nonce + "@example.test", "password": testPassword},
		http.StatusUnauthorized,
	)
	wrongPassword := api.do(
		t,
		http.MethodPost,
		"/api/v1/auth/login",
		"",
		"",
		map[string]any{"email": emailA, "password": "wrong-password"},
		http.StatusUnauthorized,
	)
	missingError := assertAPIError(t, missingLogin, auth.CodeInvalidCredentials, "invalid credentials")
	wrongError := assertAPIError(t, wrongPassword, auth.CodeInvalidCredentials, "invalid credentials")
	if missingError.Code != wrongError.Code || missingError.Message != wrongError.Message {
		t.Fatal("missing email and wrong password must have indistinguishable public errors")
	}

	loginA := decodeResponse[loginResponse](t, api.do(
		t, http.MethodPost, "/api/v1/auth/login", "", "",
		map[string]any{"email": emailA, "password": testPassword}, http.StatusOK,
	))
	loginB := decodeResponse[loginResponse](t, api.do(
		t, http.MethodPost, "/api/v1/auth/login", "", "",
		map[string]any{"email": emailB, "password": testPassword}, http.StatusOK,
	))
	if loginA.AccessToken == "" || loginB.AccessToken == "" ||
		loginA.TokenType != "Bearer" || loginB.TokenType != "Bearer" {
		t.Fatal("login: expected non-empty Bearer access tokens")
	}
	unauthorized := api.do(
		t, http.MethodGet, "/api/v1/me", loginA.AccessToken+"tampered", "", nil, http.StatusUnauthorized,
	)
	assertAPIError(t, unauthorized, httpx.CodeUnauthorized, "unauthorized")

	profileA := decodeResponse[userResponse](t, api.do(
		t,
		http.MethodPatch,
		"/api/v1/me",
		loginA.AccessToken,
		"",
		map[string]any{
			"timezone":             "Asia/Shanghai",
			"digest_time":          "09:30",
			"max_items_per_digest": 25,
		},
		http.StatusOK,
	))
	if profileA.Timezone != "Asia/Shanghai" || profileA.DigestTime != "09:30" ||
		profileA.MaxItemsPerDigest != 25 {
		t.Fatalf("profile update: unexpected public values %+v", profileA)
	}

	sourcesResult := api.do(
		t, http.MethodGet, "/api/v1/sources", loginA.AccessToken, "", nil, http.StatusOK,
	)
	if strings.Contains(string(sourcesResult.body), "endpoint") ||
		strings.Contains(string(sourcesResult.body), "config_json") ||
		strings.Contains(string(sourcesResult.body), "export.arxiv.org") {
		t.Fatal("source catalog exposed a persistence-only source field")
	}
	sources := decodeResponse[[]sourceResponse](t, sourcesResult)
	var arXiv sourceResponse
	for _, item := range sources {
		if item.SourceKey == "arxiv" {
			arXiv = item
			break
		}
	}
	if arXiv.ID == 0 || len(arXiv.AllowedCategories) == 0 || len(arXiv.RuleTypes) != 4 {
		t.Fatalf("source catalog: arXiv public capabilities are incomplete")
	}

	disabledSource := source.Source{
		SourceKey: disabledSourceKey,
		Kind:      source.KindArXiv,
		Name:      "Disabled M1 test source",
		Enabled:   false,
		ConfigJSON: json.RawMessage(
			`{"allowed_categories":["cs.AI"],"rule_types":["category"]}`,
		),
	}
	if err := database.Create(&disabledSource).Error; err != nil {
		t.Fatalf("create isolated disabled source fixture: %v", err)
	}
	disabledCreate := api.do(
		t,
		http.MethodPost,
		"/api/v1/subscriptions",
		loginA.AccessToken,
		"",
		createSubscriptionBody(disabledSource.ID, "disabled source", true, []string{"cs.AI"}),
		http.StatusNotFound,
	)
	assertAPIError(t, disabledCreate, source.CodeSourceNotFound, "source not found")

	duplicateRulesBody := createSubscriptionBody(arXiv.ID, "duplicate rules", true, []string{"cs.AI"})
	duplicateRulesBody["rules"].(map[string]any)["authors"] = []string{"Agent Smith", " agent  smith "}
	duplicateRules := api.do(
		t,
		http.MethodPost,
		"/api/v1/subscriptions",
		loginA.AccessToken,
		"",
		duplicateRulesBody,
		http.StatusBadRequest,
	)
	assertAPIError(t, duplicateRules, httpx.CodeValidationError, "subscription is invalid")

	createdA := decodeResponse[subscriptionResponse](t, api.do(
		t,
		http.MethodPost,
		"/api/v1/subscriptions",
		loginA.AccessToken,
		"",
		map[string]any{
			"source_id": arXiv.ID,
			"name":      "Agent papers",
			"objective": "Track useful agent systems",
			"enabled":   true,
			"rules": map[string]any{
				"categories":       []string{"cs.ai"},
				"authors":          []string{" Jane  Doe "},
				"include_keywords": []string{"Tool Use"},
				"exclude_keywords": []string{"Survey"},
			},
		},
		http.StatusCreated,
	))
	if createdA.ID == 0 || createdA.Version != 1 || createdA.Source.ID != arXiv.ID ||
		createdA.Objective == nil || createdA.Rules.Categories[0] != "cs.AI" ||
		createdA.Rules.Authors[0] != "Jane Doe" {
		t.Fatalf("create subscription A: normalization or persistence response is incorrect")
	}

	getAResponse := api.do(
		t, http.MethodGet, fmt.Sprintf("/api/v1/subscriptions/%d", createdA.ID),
		loginA.AccessToken, "", nil, http.StatusOK,
	)
	getA := decodeResponse[subscriptionResponse](t, getAResponse)
	if getAResponse.etag != `"1"` || getA.ID != createdA.ID {
		t.Fatalf("get subscription A: expected ETag version 1")
	}

	createdB := decodeResponse[subscriptionResponse](t, api.do(
		t,
		http.MethodPost,
		"/api/v1/subscriptions",
		loginB.AccessToken,
		"",
		createSubscriptionBody(arXiv.ID, "B subscription 1", true, []string{"cs.AI"}),
		http.StatusCreated,
	))
	foreignPath := fmt.Sprintf("/api/v1/subscriptions/%d", createdB.ID)
	assertAPIError(
		t,
		api.do(t, http.MethodGet, foreignPath, loginA.AccessToken, "", nil, http.StatusNotFound),
		subscription.CodeSubscriptionNotFound,
		"subscription not found",
	)
	assertAPIError(
		t,
		api.do(
			t, http.MethodPatch, foreignPath, loginA.AccessToken, `"1"`,
			map[string]any{"name": "forbidden"}, http.StatusNotFound,
		),
		subscription.CodeSubscriptionNotFound,
		"subscription not found",
	)
	assertAPIError(
		t,
		api.do(t, http.MethodDelete, foreignPath, loginA.AccessToken, `"1"`, nil, http.StatusNotFound),
		subscription.CodeSubscriptionNotFound,
		"subscription not found",
	)

	replacementRules := map[string]any{
		"categories":       []string{"cs.CL"},
		"authors":          []string{},
		"include_keywords": []string{"Agents"},
		"exclude_keywords": []string{},
	}
	updatedAResponse := api.do(
		t,
		http.MethodPatch,
		fmt.Sprintf("/api/v1/subscriptions/%d", createdA.ID),
		loginA.AccessToken,
		`"1"`,
		map[string]any{"name": "Updated agent papers", "rules": replacementRules},
		http.StatusOK,
	)
	updatedA := decodeResponse[subscriptionResponse](t, updatedAResponse)
	if updatedAResponse.etag != `"2"` || updatedA.Version != 2 ||
		updatedA.Name != "Updated agent papers" || len(updatedA.Rules.Categories) != 1 ||
		updatedA.Rules.Categories[0] != "cs.CL" || len(updatedA.Rules.Authors) != 0 {
		t.Fatal("update subscription A: expected complete replacement and version 2")
	}

	stale := api.do(
		t,
		http.MethodPatch,
		fmt.Sprintf("/api/v1/subscriptions/%d", createdA.ID),
		loginA.AccessToken,
		`"1"`,
		map[string]any{"name": "stale overwrite"},
		http.StatusConflict,
	)
	assertAPIError(t, stale, subscription.CodeSubscriptionVersionConflict, "subscription version conflict")

	noOpResponse := api.do(
		t,
		http.MethodPatch,
		fmt.Sprintf("/api/v1/subscriptions/%d", createdA.ID),
		loginA.AccessToken,
		`"2"`,
		map[string]any{"name": "Updated agent papers", "rules": replacementRules},
		http.StatusOK,
	)
	noOp := decodeResponse[subscriptionResponse](t, noOpResponse)
	if noOp.Version != 2 || noOpResponse.etag != `"2"` {
		t.Fatal("idempotent subscription update unexpectedly incremented the version")
	}

	var duplicateRuleError *mysqldriver.MySQLError
	ruleInsertError := database.Create(&subscription.Rule{
		SubscriptionID:  createdA.ID,
		RuleType:        source.RuleTypeCategory,
		RuleValue:       "cs.CL",
		NormalizedValue: "cs.CL",
	}).Error
	if !errors.As(ruleInsertError, &duplicateRuleError) || duplicateRuleError.Number != 1062 {
		t.Fatalf("database rule uniqueness: expected MySQL duplicate-key error")
	}

	pausedAResponse := api.do(
		t,
		http.MethodPatch,
		fmt.Sprintf("/api/v1/subscriptions/%d", createdA.ID),
		loginA.AccessToken,
		`"2"`,
		map[string]any{"enabled": false},
		http.StatusOK,
	)
	pausedA := decodeResponse[subscriptionResponse](t, pausedAResponse)
	if pausedA.Enabled || pausedA.Version != 3 || pausedAResponse.etag != `"3"` ||
		len(pausedA.Rules.Categories) != 1 {
		t.Fatal("pause subscription A: expected version 3 with preserved rules")
	}
	pausedPage := decodeResponse[subscriptionPageResponse](t, api.do(
		t,
		http.MethodGet,
		"/api/v1/subscriptions?enabled=false",
		loginA.AccessToken,
		"",
		nil,
		http.StatusOK,
	))
	if pausedPage.Total != 1 || len(pausedPage.Items) != 1 || pausedPage.Items[0].ID != createdA.ID {
		t.Fatal("paused subscription list filter returned an unexpected result")
	}

	for index := 2; index <= 20; index++ {
		api.do(
			t,
			http.MethodPost,
			"/api/v1/subscriptions",
			loginB.AccessToken,
			"",
			createSubscriptionBody(
				arXiv.ID, fmt.Sprintf("B subscription %d", index), true, []string{"cs.AI"},
			),
			http.StatusCreated,
		)
	}
	limitReached := api.do(
		t,
		http.MethodPost,
		"/api/v1/subscriptions",
		loginB.AccessToken,
		"",
		createSubscriptionBody(arXiv.ID, "B subscription 21", true, []string{"cs.AI"}),
		http.StatusConflict,
	)
	assertAPIError(
		t, limitReached, subscription.CodeSubscriptionLimitReached, "enabled subscription limit reached",
	)

	api.do(
		t,
		http.MethodDelete,
		fmt.Sprintf("/api/v1/subscriptions/%d", createdA.ID),
		loginA.AccessToken,
		`"3"`,
		nil,
		http.StatusNoContent,
	)
	assertAPIError(
		t,
		api.do(
			t, http.MethodGet, fmt.Sprintf("/api/v1/subscriptions/%d", createdA.ID),
			loginA.AccessToken, "", nil, http.StatusNotFound,
		),
		subscription.CodeSubscriptionNotFound,
		"subscription not found",
	)
	var stored subscription.Subscription
	if err := database.Where("id = ?", createdA.ID).Take(&stored).Error; err != nil {
		t.Fatalf("soft delete database verification: load subscription: %v", err)
	}
	var storedRuleCount int64
	if err := database.Model(&subscription.Rule{}).
		Where("subscription_id = ?", createdA.ID).
		Count(&storedRuleCount).Error; err != nil {
		t.Fatalf("soft delete database verification: count rules: %v", err)
	}
	if stored.DeletedAt == nil || stored.Version != 4 || storedRuleCount == 0 {
		t.Fatal("soft delete must preserve the subscription and replacement rules while incrementing version")
	}

	logText := logs.String()
	for _, secret := range []string{
		testPassword,
		loginA.AccessToken,
		loginB.AccessToken,
		parsedDSN.Passwd,
		os.Getenv(testDSNEnv),
	} {
		if secret != "" && strings.Contains(logText, secret) {
			t.Fatal("integration logs contain a credential, token, or DSN")
		}
	}
}

func openM1TestDatabase(t *testing.T) (*gorm.DB, *sql.DB, *mysqldriver.Config) {
	t.Helper()
	rawDSN, exists := os.LookupEnv(testDSNEnv)
	if !exists || strings.TrimSpace(rawDSN) == "" {
		t.Skip("set M1_TEST_MYSQL_DSN to a migrated, dedicated database whose name ends in _test")
	}
	parsed, err := mysqldriver.ParseDSN(rawDSN)
	if err != nil {
		t.Fatalf("parse M1 test DSN without exposing it: %v", err)
	}
	if parsed.DBName == "" || !strings.HasSuffix(strings.ToLower(parsed.DBName), "_test") {
		t.Fatalf("M1_TEST_MYSQL_DSN must select a dedicated database whose name ends in _test")
	}
	database, err := platformdb.Open(config.Config{
		MySQLDSN:          rawDSN,
		MySQLMaxOpenConns: 10,
		MySQLMaxIdleConns: 5,
	})
	if err != nil {
		t.Fatalf("open isolated M1 test database without exposing its DSN: %v", err)
	}
	sqlDB, err := database.DB()
	if err != nil {
		t.Fatalf("get isolated M1 test connection pool: %v", err)
	}
	return database, sqlDB, parsed
}

func assertM1Schema(t *testing.T, database *gorm.DB) {
	t.Helper()
	for _, table := range []any{user.User{}, source.Source{}, subscription.Subscription{}, subscription.Rule{}} {
		if !database.Migrator().HasTable(table) {
			t.Fatalf("M1 test database is not migrated: required table for %T is missing", table)
		}
	}
	for _, column := range []string{"source_id", "objective", "version", "deleted_at"} {
		if !database.Migrator().HasColumn(&subscription.Subscription{}, column) {
			t.Fatalf("M1 test database is not migrated: subscriptions.%s is missing", column)
		}
	}
	if !database.Migrator().HasColumn(&user.User{}, "max_items_per_digest") {
		t.Fatal("M1 test database is not migrated: users.max_items_per_digest is missing")
	}
}

func newM1TestAPI(
	t *testing.T,
	database *gorm.DB,
	mysqlCheck server.DependencyCheck,
	logs *bytes.Buffer,
) testAPI {
	t.Helper()
	logger := slog.New(slog.NewJSONHandler(logs, nil))
	userRepository := user.NewRepository(database)
	userService := user.NewService(userRepository)
	userHandler := user.NewHandler(userService, logger)
	tokenService, err := auth.NewTokenService(
		[]byte("m1-integration-jwt-secret-at-least-32-bytes"),
		"signalwatch-m1-integration",
		15*time.Minute,
		time.Now,
	)
	if err != nil {
		t.Fatalf("create M1 token service: %v", err)
	}
	authHandler := auth.NewHandler(auth.NewService(userRepository, tokenService), logger)
	sourceService := source.NewService(source.NewRepository(database))
	sourceHandler := source.NewHandler(sourceService, logger)
	subscriptionService := subscription.NewService(subscription.NewRepository(database), sourceService)
	subscriptionHandler := subscription.NewHandler(subscriptionService, logger)
	router, err := server.NewRouter(server.Dependencies{
		AppEnv:                    "test",
		ServiceName:               "signalwatch-api",
		Logger:                    logger,
		MySQLCheck:                mysqlCheck,
		RedisCheck:                func(context.Context) error { return nil },
		RegisterHandler:           userHandler.Register,
		LoginHandler:              authHandler.Login,
		AuthMiddleware:            auth.Middleware(tokenService),
		GetProfileHandler:         userHandler.GetProfile,
		UpdateProfileHandler:      userHandler.UpdateProfile,
		ListSourcesHandler:        sourceHandler.List,
		GetSourceHandler:          sourceHandler.Get,
		CreateSubscriptionHandler: subscriptionHandler.Create,
		ListSubscriptionsHandler:  subscriptionHandler.List,
		GetSubscriptionHandler:    subscriptionHandler.Get,
		UpdateSubscriptionHandler: subscriptionHandler.Update,
		DeleteSubscriptionHandler: subscriptionHandler.Delete,
	})
	if err != nil {
		t.Fatalf("create in-process M1 API: %v", err)
	}
	return testAPI{router: router}
}

func (api testAPI) do(
	t *testing.T,
	method string,
	path string,
	token string,
	ifMatch string,
	body any,
	wantStatus int,
) testResponse {
	t.Helper()
	var encodedBody []byte
	var err error
	if body != nil {
		encodedBody, err = json.Marshal(body)
		if err != nil {
			t.Fatalf("%s %s: encode request fixture: %v", method, path, err)
		}
	}
	request := httptest.NewRequest(method, path, bytes.NewReader(encodedBody))
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	if ifMatch != "" {
		request.Header.Set("If-Match", ifMatch)
	}
	recorder := httptest.NewRecorder()
	api.router.ServeHTTP(recorder, request)
	requestID := recorder.Header().Get(httpx.HeaderRequestID)
	if requestID == "" {
		t.Fatalf("%s %s: response is missing request ID", method, path)
	}
	if recorder.Code != wantStatus {
		t.Fatalf(
			"%s %s: expected status %d, got %d (request_id=%s)",
			method,
			path,
			wantStatus,
			recorder.Code,
			requestID,
		)
	}
	return testResponse{
		status: recorder.Code, requestID: requestID,
		etag: recorder.Header().Get("ETag"), body: append([]byte(nil), recorder.Body.Bytes()...),
	}
}

func decodeResponse[T any](t *testing.T, response testResponse) T {
	t.Helper()
	var decoded T
	if err := json.Unmarshal(response.body, &decoded); err != nil {
		t.Fatalf("decode API response (status=%d request_id=%s): %v", response.status, response.requestID, err)
	}
	return decoded
}

func assertAPIError(t *testing.T, response testResponse, code, message string) errorResponse {
	t.Helper()
	decoded := decodeResponse[errorResponse](t, response)
	if decoded.Code != code || decoded.Message != message || decoded.RequestID != response.requestID {
		t.Fatalf(
			"unexpected API error contract: status=%d code=%q request_id=%s",
			response.status,
			decoded.Code,
			response.requestID,
		)
	}
	return decoded
}

func createSubscriptionBody(
	sourceID uint64,
	name string,
	enabled bool,
	categories []string,
) map[string]any {
	return map[string]any{
		"source_id": sourceID,
		"name":      name,
		"enabled":   enabled,
		"rules": map[string]any{
			"categories":       categories,
			"authors":          []string{},
			"include_keywords": []string{},
			"exclude_keywords": []string{},
		},
	}
}

func cleanupM1TestData(
	t *testing.T,
	database *gorm.DB,
	emails []string,
	disabledSourceKey string,
) {
	t.Helper()
	var userIDs []uint64
	if err := database.Model(&user.User{}).Where("email IN ?", emails).Pluck("id", &userIDs).Error; err != nil {
		t.Errorf("cleanup isolated M1 users lookup: %v", err)
		return
	}
	if len(userIDs) > 0 {
		if err := database.Exec(
			"DELETE FROM subscription_rules WHERE subscription_id IN (SELECT id FROM subscriptions WHERE user_id IN ?)",
			userIDs,
		).Error; err != nil {
			t.Errorf("cleanup isolated M1 subscription rules: %v", err)
			return
		}
		if err := database.Where("user_id IN ?", userIDs).Delete(&subscription.Subscription{}).Error; err != nil {
			t.Errorf("cleanup isolated M1 subscriptions: %v", err)
			return
		}
		if err := database.Where("id IN ?", userIDs).Delete(&user.User{}).Error; err != nil {
			t.Errorf("cleanup isolated M1 users: %v", err)
			return
		}
	}
	if err := database.Where("source_key = ?", disabledSourceKey).Delete(&source.Source{}).Error; err != nil {
		t.Errorf("cleanup isolated M1 source fixture: %v", err)
	}
}
