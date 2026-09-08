package subscription

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"signalwatch/internal/platform/httpx"
	"signalwatch/internal/source"
)

const createSubscriptionPath = "/api/v1/subscriptions"

func TestHandlerCreateUsesAuthenticatedUserAndReturnsCreated(t *testing.T) {
	var capturedUserID uint64
	var captured CreateInput
	service := creationServiceStub{create: func(_ context.Context, userID uint64, input CreateInput) (PublicSubscription, error) {
		capturedUserID = userID
		captured = input
		return PublicSubscription{ID: 88, Source: arXivCatalog(), Name: "Agent papers", Enabled: true,
			Version: 1, Rules: PublicRules{Categories: []string{"cs.AI"}, Authors: []string{},
				IncludeKeywords: []string{}, ExcludeKeywords: []string{}}}, nil
	}}
	router, _ := newSubscriptionTestRouter(service, true)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, createSubscriptionPath, strings.NewReader(
		`{"source_id":1,"name":"Agent papers","rules":{"categories":["cs.ai"]}}`,
	))
	request.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", recorder.Code, recorder.Body.String())
	}
	if capturedUserID != 42 || captured.SourceID != 1 || captured.Enabled != nil ||
		len(captured.Rules.Categories) != 1 {
		t.Fatalf("unexpected service input user=%d input=%+v", capturedUserID, captured)
	}
	var response PublicSubscription
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil || response.ID != 88 {
		t.Fatalf("unexpected response %s: %v", recorder.Body.String(), err)
	}
}

func TestHandlerCreateRequiresAuthenticationContext(t *testing.T) {
	calls := 0
	router, _ := newSubscriptionTestRouter(creationServiceStub{create: func(context.Context, uint64, CreateInput) (PublicSubscription, error) {
		calls++
		return PublicSubscription{}, nil
	}}, false)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, createSubscriptionPath, strings.NewReader(`{}`)))
	assertSubscriptionError(t, recorder, http.StatusUnauthorized, httpx.CodeUnauthorized)
	if calls != 0 {
		t.Fatal("unauthenticated request reached service")
	}
}

func TestHandlerCreateStrictlyRejectsForgedSourceFields(t *testing.T) {
	calls := 0
	service := creationServiceStub{create: func(context.Context, uint64, CreateInput) (PublicSubscription, error) {
		calls++
		return PublicSubscription{}, nil
	}}
	for _, forbidden := range []string{"endpoint", "source_key"} {
		router, _ := newSubscriptionTestRouter(service, true)
		body := `{"source_id":1,"name":"valid","` + forbidden + `":"forged","rules":{"categories":["cs.AI"]}}`
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, createSubscriptionPath, strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(recorder, request)
		assertSubscriptionError(t, recorder, http.StatusBadRequest, httpx.CodeValidationError)
	}
	if calls != 0 {
		t.Fatal("forged source fields must be rejected before service")
	}
}

func TestHandlerCreateMapsExpectedFailures(t *testing.T) {
	tests := []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{"source missing", source.ErrNotFound, http.StatusNotFound, source.CodeSourceNotFound},
		{"limit", ErrLimitReached, http.StatusConflict, CodeSubscriptionLimitReached},
		{"invalid", ErrDuplicateRule, http.StatusBadRequest, httpx.CodeValidationError},
		{"inactive user", ErrUserNotFound, http.StatusUnauthorized, httpx.CodeUnauthorized},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			router, _ := newSubscriptionTestRouter(creationServiceStub{create: func(context.Context, uint64, CreateInput) (PublicSubscription, error) {
				return PublicSubscription{}, test.err
			}}, true)
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPost, createSubscriptionPath, strings.NewReader(
				`{"source_id":1,"name":"valid","rules":{"categories":["cs.AI"]}}`,
			))
			request.Header.Set("Content-Type", "application/json")
			router.ServeHTTP(recorder, request)
			assertSubscriptionError(t, recorder, test.status, test.code)
		})
	}
}

func TestHandlerCreateHidesUnexpectedFailure(t *testing.T) {
	service := creationServiceStub{create: func(context.Context, uint64, CreateInput) (PublicSubscription, error) {
		return PublicSubscription{}, errors.New("database detail must stay internal")
	}}
	router, logs := newSubscriptionTestRouter(service, true)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, createSubscriptionPath, strings.NewReader(
		`{"source_id":1,"name":"valid","rules":{"categories":["cs.AI"]}}`,
	))
	request.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(recorder, request)
	assertSubscriptionError(t, recorder, http.StatusInternalServerError, httpx.CodeInternalError)
	if strings.Contains(recorder.Body.String(), "database detail") || !strings.Contains(logs.String(), "database detail") {
		t.Fatal("expected internal error only in server log")
	}
}

func TestHandlerListUsesDefaultsAndAuthenticatedUser(t *testing.T) {
	var gotUserID uint64
	var gotInput ListInput
	service := subscriptionServiceStub{list: func(_ context.Context, userID uint64, input ListInput) (ListResult, error) {
		gotUserID, gotInput = userID, input
		return ListResult{Items: []PublicSubscription{}, Page: input.Page, PageSize: input.PageSize}, nil
	}}
	router, _ := newSubscriptionTestRouter(service, true)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, createSubscriptionPath, nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", recorder.Code, recorder.Body.String())
	}
	if gotUserID != 42 || gotInput.Page != DefaultPage || gotInput.PageSize != DefaultPageSize ||
		gotInput.Filter.Enabled != nil || gotInput.Filter.SourceID != nil {
		t.Fatalf("unexpected default query user=%d input=%+v", gotUserID, gotInput)
	}
	if recorder.Body.String() != `{"items":[],"page":1,"page_size":20,"total":0}` {
		t.Fatalf("unexpected empty page response %s", recorder.Body.String())
	}
}

func TestHandlerListParsesEnabledSourceAndPagination(t *testing.T) {
	var got ListInput
	service := subscriptionServiceStub{list: func(_ context.Context, _ uint64, input ListInput) (ListResult, error) {
		got = input
		return ListResult{Items: []PublicSubscription{}, Page: input.Page, PageSize: input.PageSize}, nil
	}}
	router, _ := newSubscriptionTestRouter(service, true)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(
		http.MethodGet, createSubscriptionPath+"?page=3&page_size=100&enabled=false&source_id=7", nil,
	))

	if recorder.Code != http.StatusOK || got.Page != 3 || got.PageSize != 100 ||
		got.Filter.Enabled == nil || *got.Filter.Enabled ||
		got.Filter.SourceID == nil || *got.Filter.SourceID != 7 {
		t.Fatalf("unexpected parsed query status=%d input=%+v body=%s", recorder.Code, got, recorder.Body.String())
	}
}

func TestHandlerListRejectsInvalidQueries(t *testing.T) {
	calls := 0
	service := subscriptionServiceStub{list: func(context.Context, uint64, ListInput) (ListResult, error) {
		calls++
		return ListResult{}, nil
	}}
	for _, query := range []string{
		"page=0", "page=-1", "page=", "page_size=0", "page_size=101",
		"enabled=abc", "enabled=1", "source_id=0", "source_id=-1", "source_id=abc",
	} {
		t.Run(query, func(t *testing.T) {
			router, _ := newSubscriptionTestRouter(service, true)
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, createSubscriptionPath+"?"+query, nil))
			assertSubscriptionError(t, recorder, http.StatusBadRequest, httpx.CodeValidationError)
		})
	}
	if calls != 0 {
		t.Fatalf("invalid queries reached service %d times", calls)
	}
}

func TestHandlerGetReturnsETagAndSafeCompleteRules(t *testing.T) {
	createdAt := time.Date(2026, time.September, 4, 1, 2, 3, 0, time.UTC)
	service := subscriptionServiceStub{get: func(_ context.Context, userID, id uint64) (PublicSubscription, error) {
		if userID != 42 || id != 9 {
			t.Fatalf("unexpected get arguments user=%d id=%d", userID, id)
		}
		return PublicSubscription{
			ID: 9, Source: arXivCatalog(), Name: "papers", Enabled: true, Version: 7,
			Rules: PublicRules{Categories: []string{"cs.AI"}, Authors: []string{},
				IncludeKeywords: []string{}, ExcludeKeywords: []string{}},
			CreatedAt: createdAt, UpdatedAt: createdAt,
		}, nil
	}}
	router, _ := newSubscriptionTestRouter(service, true)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, createSubscriptionPath+"/9", nil))

	if recorder.Code != http.StatusOK || recorder.Header().Get("ETag") != `"7"` {
		t.Fatalf("expected 200 and ETag, got status=%d etag=%q body=%s", recorder.Code, recorder.Header().Get("ETag"), recorder.Body.String())
	}
	for _, field := range []string{`"categories":["cs.AI"]`, `"authors":[]`, `"include_keywords":[]`, `"exclude_keywords":[]`} {
		if !strings.Contains(recorder.Body.String(), field) {
			t.Fatalf("expected response field %s in %s", field, recorder.Body.String())
		}
	}
}

func TestHandlerGetMapsMissingForeignAndDeletedToSameNotFound(t *testing.T) {
	service := subscriptionServiceStub{get: func(context.Context, uint64, uint64) (PublicSubscription, error) {
		return PublicSubscription{}, ErrNotFound
	}}
	for _, id := range []string{"8", "999999"} {
		router, _ := newSubscriptionTestRouter(service, true)
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, createSubscriptionPath+"/"+id, nil))
		assertSubscriptionError(t, recorder, http.StatusNotFound, CodeSubscriptionNotFound)
	}
}

func TestHandlerGetRejectsInvalidID(t *testing.T) {
	calls := 0
	service := subscriptionServiceStub{get: func(context.Context, uint64, uint64) (PublicSubscription, error) {
		calls++
		return PublicSubscription{}, nil
	}}
	for _, id := range []string{"0", "-1", "abc"} {
		router, _ := newSubscriptionTestRouter(service, true)
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, createSubscriptionPath+"/"+id, nil))
		assertSubscriptionError(t, recorder, http.StatusBadRequest, httpx.CodeValidationError)
	}
	if calls != 0 {
		t.Fatal("invalid subscription id reached service")
	}
}

func TestHandlerUpdateParsesIfMatchPartialFieldsAndCompleteRules(t *testing.T) {
	var gotUserID, gotID uint64
	var gotVersion uint32
	var gotInput UpdateInput
	service := subscriptionServiceStub{update: func(
		_ context.Context,
		userID uint64,
		id uint64,
		version uint32,
		input UpdateInput,
	) (PublicSubscription, error) {
		gotUserID, gotID, gotVersion, gotInput = userID, id, version, input
		return PublicSubscription{
			ID: 9, Source: arXivCatalog(), Name: "papers", Version: 4,
			Rules: PublicRules{Categories: []string{"cs.CL"}, Authors: []string{},
				IncludeKeywords: []string{}, ExcludeKeywords: []string{}},
		}, nil
	}}
	router, _ := newSubscriptionTestRouter(service, true)
	request := httptest.NewRequest(http.MethodPatch, createSubscriptionPath+"/9", strings.NewReader(
		`{"objective":null,"enabled":false,"rules":{"categories":["cs.CL"],"include_keywords":[]}}`,
	))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("If-Match", `"3"`)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK || recorder.Header().Get("ETag") != `"4"` {
		t.Fatalf("expected 200 and new ETag, got status=%d etag=%q body=%s", recorder.Code, recorder.Header().Get("ETag"), recorder.Body.String())
	}
	if gotUserID != 42 || gotID != 9 || gotVersion != 3 || gotInput.Name != nil ||
		!gotInput.ObjectiveSet || gotInput.Objective != nil || gotInput.Enabled == nil || *gotInput.Enabled ||
		gotInput.Rules == nil || len(gotInput.Rules.Categories) != 1 ||
		len(gotInput.Rules.Authors) != 0 || len(gotInput.Rules.ExcludeKeywords) != 0 {
		t.Fatalf("unexpected update input user=%d id=%d version=%d input=%+v", gotUserID, gotID, gotVersion, gotInput)
	}
}

func TestHandlerUpdateRejectsMissingInvalidIfMatch(t *testing.T) {
	calls := 0
	service := subscriptionServiceStub{update: func(context.Context, uint64, uint64, uint32, UpdateInput) (PublicSubscription, error) {
		calls++
		return PublicSubscription{}, nil
	}}
	for _, value := range []string{"", "3", `W/"3"`, `"0"`, `"-1"`, `"abc"`, `"4294967296"`, `"3","4"`} {
		t.Run(value, func(t *testing.T) {
			router, _ := newSubscriptionTestRouter(service, true)
			request := httptest.NewRequest(http.MethodPatch, createSubscriptionPath+"/9", strings.NewReader(`{"name":"new"}`))
			request.Header.Set("If-Match", value)
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, request)
			assertSubscriptionError(t, recorder, http.StatusBadRequest, httpx.CodeValidationError)
		})
	}
	if calls != 0 {
		t.Fatalf("invalid If-Match reached service %d times", calls)
	}
}

func TestHandlerUpdateStrictlyRejectsEmptyForbiddenAndIncompleteBodies(t *testing.T) {
	calls := 0
	service := subscriptionServiceStub{update: func(context.Context, uint64, uint64, uint32, UpdateInput) (PublicSubscription, error) {
		calls++
		return PublicSubscription{}, nil
	}}
	for _, body := range []string{
		`{}`,
		`{"source_id":2}`,
		`{"enabled":null}`,
		`{"rules":null}`,
		`{"rules":{"categories":["cs.AI"],"authors":[],"include_keywords":[]}}`,
		`{"rules":{"categories":["cs.AI"],"authors":[],"include_keywords":[],"exclude_keywords":[],"extra":[]}}`,
	} {
		t.Run(body, func(t *testing.T) {
			router, _ := newSubscriptionTestRouter(service, true)
			request := httptest.NewRequest(http.MethodPatch, createSubscriptionPath+"/9", strings.NewReader(body))
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("If-Match", `"3"`)
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, request)
			assertSubscriptionError(t, recorder, http.StatusBadRequest, httpx.CodeValidationError)
		})
	}
	if calls != 0 {
		t.Fatalf("invalid PATCH bodies reached service %d times", calls)
	}
}

func TestHandlerUpdateMapsConflictLimitNotFoundAndValidation(t *testing.T) {
	for _, test := range []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{name: "conflict", err: ErrVersionConflict, status: http.StatusConflict, code: CodeSubscriptionVersionConflict},
		{name: "quota", err: ErrLimitReached, status: http.StatusConflict, code: CodeSubscriptionLimitReached},
		{name: "foreign missing deleted", err: ErrNotFound, status: http.StatusNotFound, code: CodeSubscriptionNotFound},
		{name: "invalid rules", err: ErrCategoryRequired, status: http.StatusBadRequest, code: httpx.CodeValidationError},
	} {
		t.Run(test.name, func(t *testing.T) {
			service := subscriptionServiceStub{update: func(context.Context, uint64, uint64, uint32, UpdateInput) (PublicSubscription, error) {
				return PublicSubscription{}, test.err
			}}
			router, _ := newSubscriptionTestRouter(service, true)
			request := httptest.NewRequest(http.MethodPatch, createSubscriptionPath+"/9", strings.NewReader(`{"name":"new"}`))
			request.Header.Set("If-Match", `"3"`)
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, request)
			assertSubscriptionError(t, recorder, test.status, test.code)
		})
	}
}

func TestHandlerDeleteUsesOwnedVersionAndReturnsEmpty204(t *testing.T) {
	var gotUserID, gotID uint64
	var gotVersion uint32
	service := subscriptionServiceStub{delete: func(_ context.Context, userID, id uint64, version uint32) error {
		gotUserID, gotID, gotVersion = userID, id, version
		return nil
	}}
	router, _ := newSubscriptionTestRouter(service, true)
	request := httptest.NewRequest(http.MethodDelete, createSubscriptionPath+"/9", nil)
	request.Header.Set("If-Match", `"3"`)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusNoContent || recorder.Body.Len() != 0 ||
		gotUserID != 42 || gotID != 9 || gotVersion != 3 {
		t.Fatalf("unexpected delete response status=%d body=%q scope=%d/%d/%d", recorder.Code, recorder.Body.String(), gotUserID, gotID, gotVersion)
	}
}

func TestHandlerDeleteMapsRepeatedOrForeignToNotFoundAndStaleToConflict(t *testing.T) {
	for _, test := range []struct {
		err    error
		status int
		code   string
	}{
		{err: ErrNotFound, status: http.StatusNotFound, code: CodeSubscriptionNotFound},
		{err: ErrVersionConflict, status: http.StatusConflict, code: CodeSubscriptionVersionConflict},
	} {
		service := subscriptionServiceStub{delete: func(context.Context, uint64, uint64, uint32) error {
			return test.err
		}}
		router, _ := newSubscriptionTestRouter(service, true)
		request := httptest.NewRequest(http.MethodDelete, createSubscriptionPath+"/9", nil)
		request.Header.Set("If-Match", `"3"`)
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, request)
		assertSubscriptionError(t, recorder, test.status, test.code)
	}
}

type creationServiceStub struct {
	create func(context.Context, uint64, CreateInput) (PublicSubscription, error)
}

func (stub creationServiceStub) Create(ctx context.Context, userID uint64, input CreateInput) (PublicSubscription, error) {
	return stub.create(ctx, userID, input)
}

func (creationServiceStub) List(context.Context, uint64, ListInput) (ListResult, error) {
	return ListResult{}, nil
}

func (creationServiceStub) Get(context.Context, uint64, uint64) (PublicSubscription, error) {
	return PublicSubscription{}, nil
}

func (creationServiceStub) Update(context.Context, uint64, uint64, uint32, UpdateInput) (PublicSubscription, error) {
	return PublicSubscription{}, nil
}

func (creationServiceStub) Delete(context.Context, uint64, uint64, uint32) error { return nil }

type subscriptionServiceStub struct {
	create func(context.Context, uint64, CreateInput) (PublicSubscription, error)
	list   func(context.Context, uint64, ListInput) (ListResult, error)
	get    func(context.Context, uint64, uint64) (PublicSubscription, error)
	update func(context.Context, uint64, uint64, uint32, UpdateInput) (PublicSubscription, error)
	delete func(context.Context, uint64, uint64, uint32) error
}

func (stub subscriptionServiceStub) Create(ctx context.Context, userID uint64, input CreateInput) (PublicSubscription, error) {
	if stub.create == nil {
		return PublicSubscription{}, nil
	}
	return stub.create(ctx, userID, input)
}

func (stub subscriptionServiceStub) List(ctx context.Context, userID uint64, input ListInput) (ListResult, error) {
	return stub.list(ctx, userID, input)
}

func (stub subscriptionServiceStub) Get(ctx context.Context, userID, id uint64) (PublicSubscription, error) {
	return stub.get(ctx, userID, id)
}

func (stub subscriptionServiceStub) Update(
	ctx context.Context,
	userID uint64,
	id uint64,
	version uint32,
	input UpdateInput,
) (PublicSubscription, error) {
	if stub.update == nil {
		return PublicSubscription{}, nil
	}
	return stub.update(ctx, userID, id, version, input)
}

func (stub subscriptionServiceStub) Delete(ctx context.Context, userID, id uint64, version uint32) error {
	if stub.delete == nil {
		return nil
	}
	return stub.delete(ctx, userID, id, version)
}

func newSubscriptionTestRouter(service SubscriptionService, authenticated bool) (*gin.Engine, *bytes.Buffer) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(httpx.RequestIDMiddleware())
	if authenticated {
		router.Use(func(c *gin.Context) {
			httpx.SetCurrentUserID(c, 42)
			c.Next()
		})
	}
	handler := NewHandler(service, logger)
	router.POST(createSubscriptionPath, handler.Create)
	router.GET(createSubscriptionPath, handler.List)
	router.GET(createSubscriptionPath+"/:id", handler.Get)
	router.PATCH(createSubscriptionPath+"/:id", handler.Update)
	router.DELETE(createSubscriptionPath+"/:id", handler.Delete)
	return router, &logs
}

func assertSubscriptionError(t *testing.T, recorder *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	if recorder.Code != status {
		t.Fatalf("expected status %d, got %d: %s", status, recorder.Code, recorder.Body.String())
	}
	var response httpx.ErrorResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil || response.Code != code {
		t.Fatalf("expected error code %q, got %s", code, recorder.Body.String())
	}
}
