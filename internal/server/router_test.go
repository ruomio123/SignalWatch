package server

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"signalwatch/internal/auth"
	"signalwatch/internal/platform/httpx"
	"signalwatch/internal/user"
)

type identityLoaderFunc func(context.Context, uint64) (user.User, error)

func (loader identityLoaderFunc) FindActiveByID(ctx context.Context, id uint64) (user.User, error) {
	return loader(ctx, id)
}

func TestNewRouterRegistersHealthRoutes(t *testing.T) {
	router := newTestRouter(t)

	tests := []struct {
		name       string
		path       string
		wantStatus int
		wantBody   string
	}{
		{
			name:       "health",
			path:       "/healthz",
			wantStatus: http.StatusOK,
			wantBody:   `{"status":"ok","service":"signalwatch-api"}`,
		},
		{
			name:       "readiness",
			path:       "/readyz",
			wantStatus: http.StatusOK,
			wantBody:   `{"status":"ready"}`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, test.path, nil)
			recorder := httptest.NewRecorder()

			router.ServeHTTP(recorder, request)

			if recorder.Code != test.wantStatus {
				t.Fatalf("expected status %d, got %d", test.wantStatus, recorder.Code)
			}

			assertJSONEqual(t, recorder.Body.Bytes(), []byte(test.wantBody))
			assertRequestIDResponseHeader(t, recorder)
		})
	}
}

func TestNewRouterServesEmbeddedFrontend(t *testing.T) {
	router := newTestRouter(t)

	for _, path := range []string{"/", "/login", "/register", "/app", "/papers", "/subscriptions", "/settings"} {
		t.Run(path, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, path, nil)
			recorder := httptest.NewRecorder()

			router.ServeHTTP(recorder, request)

			if recorder.Code != http.StatusOK {
				t.Fatalf("expected status 200, got %d", recorder.Code)
			}
			if contentType := recorder.Header().Get("Content-Type"); !strings.HasPrefix(contentType, "text/html") {
				t.Fatalf("expected HTML content type, got %q", contentType)
			}
			if !strings.Contains(recorder.Body.String(), "SignalWatch") {
				t.Fatal("expected frontend application shell")
			}
			if recorder.Header().Get("Content-Security-Policy") == "" {
				t.Fatal("expected frontend content security policy")
			}
		})
	}

	shell := httptest.NewRecorder()
	router.ServeHTTP(shell, httptest.NewRequest(http.MethodGet, "/", nil))
	css := regexp.MustCompile(`/assets/[^" ]+\.css`).FindString(shell.Body.String())
	assetRequest := httptest.NewRequest(http.MethodGet, css, nil)
	assetRecorder := httptest.NewRecorder()
	router.ServeHTTP(assetRecorder, assetRequest)
	if assetRecorder.Code != http.StatusOK {
		t.Fatalf("expected frontend asset status 200, got %d", assetRecorder.Code)
	}
	if !strings.HasPrefix(assetRecorder.Header().Get("Content-Type"), "text/css") {
		t.Fatalf(
			"expected CSS content type, got %q",
			assetRecorder.Header().Get("Content-Type"),
		)
	}
	if cacheControl := assetRecorder.Header().Get("Cache-Control"); cacheControl != "no-cache" {
		t.Fatalf("expected frontend assets to be revalidated, got Cache-Control %q", cacheControl)
	}
}

func TestNewRouterReturnsJSONForUnknownRoute(t *testing.T) {
	router := newTestRouter(t)

	tests := []string{
		"/not-found",
		"/healthz/",
	}

	for _, path := range tests {
		t.Run(path, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, path, nil)
			recorder := httptest.NewRecorder()

			router.ServeHTTP(recorder, request)

			assertErrorResponse(
				t,
				recorder,
				http.StatusNotFound,
				httpx.CodeNotFound,
				"route not found",
			)
		})
	}
}

func TestNewRouterReturnsJSONForUnsupportedMethod(t *testing.T) {
	router := newTestRouter(t)

	request := httptest.NewRequest(http.MethodPost, "/healthz", nil)
	recorder := httptest.NewRecorder()

	router.ServeHTTP(recorder, request)

	assertErrorResponse(
		t,
		recorder,
		http.StatusMethodNotAllowed,
		httpx.CodeMethodNotAllowed,
		"method not allowed",
	)
}

func TestNewRouterRegistersUserRegistrationRoute(t *testing.T) {
	dependencies := validTestDependencies()

	calls := 0
	dependencies.Accounts.RegisterHandler = func(c *gin.Context) {
		calls++
		c.Status(http.StatusCreated)
	}

	router, err := NewRouter(dependencies)
	if err != nil {
		t.Fatalf("create router: %v", err)
	}

	request := httptest.NewRequest(
		http.MethodPost,
		"/api/v2/auth/register",
		nil,
	)
	recorder := httptest.NewRecorder()

	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusCreated {
		t.Fatalf(
			"expected status %d, got %d",
			http.StatusCreated,
			recorder.Code,
		)
	}
	if calls != 1 {
		t.Fatalf("expected register handler once, got %d", calls)
	}
}

func TestNewRouterRequiresActiveAccountOnceForAllProtectedRoutes(t *testing.T) {
	d := validTestDependencies()
	active := true
	reads := 0
	loader := identityLoaderFunc(func(_ context.Context, id uint64) (user.User, error) {
		reads++
		if !active {
			return user.User{}, user.ErrNotFound
		}
		return user.User{ID: id, Status: user.StatusActive}, nil
	})
	d.Authorization.AuthMiddleware = func(c *gin.Context) { httpx.SetCurrentUserID(c, 42); c.Next() }
	d.Authorization.ActiveAccountMiddleware = auth.RequireActiveAccount(loader, d.Logger)
	d.AI.GetAIUsageHandler = func(c *gin.Context) { c.Status(http.StatusOK) }
	d.AgentHandler = func(c *gin.Context) { c.Status(http.StatusOK) }
	router, err := NewRouter(d)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/v2/me", "/api/v2/auth/probe", "/api/v2/subscriptions", "/api/v2/papers", "/api/v2/ai/usage", "/api/v2/agent/conversations"} {
		for _, state := range []bool{true, false} {
			active = state
			before := reads
			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
			want := http.StatusOK
			if !active {
				want = http.StatusUnauthorized
			}
			if response.Code != want || reads-before != 1 {
				t.Fatalf("%s active=%v: status=%d lookups=%d", path, active, response.Code, reads-before)
			}
		}
	}
}

func TestRemovedOperationsRoutesReturnJSONNotFound(t *testing.T) {
	router := newTestRouter(t)
	for _, path := range []string{"/api/v2/ops/status", "/api/v2/ops/sources", "/api/v2/ops/status/"} {
		for _, method := range []string{http.MethodGet, http.MethodPost} {
			for _, token := range []string{"", "Bearer obsolete-token"} {
				response := httptest.NewRecorder()
				request := httptest.NewRequest(method, path, nil)
				request.Header.Set("Authorization", token)
				router.ServeHTTP(response, request)
				assertErrorResponse(t, response, http.StatusNotFound, httpx.CodeNotFound, "route not found")
			}
		}
	}
}

func TestNewRouterKeepsPublicRoutesPublicAndProtectsProbe(t *testing.T) {
	dependencies := validTestDependencies()
	loginCalls := 0
	dependencies.Accounts.LoginHandler = func(c *gin.Context) {
		loginCalls++
		c.Status(http.StatusOK)
	}
	dependencies.Authorization.AuthMiddleware = func(c *gin.Context) {
		if c.GetHeader("Authorization") != "Bearer valid-token" {
			httpx.WriteError(
				c,
				http.StatusUnauthorized,
				"AUTH_UNAUTHORIZED",
				"unauthorized",
			)
			return
		}
		httpx.SetCurrentUserID(c, 42)
		c.Next()
	}

	router, err := NewRouter(dependencies)
	if err != nil {
		t.Fatalf("create router: %v", err)
	}

	loginRequest := httptest.NewRequest(http.MethodPost, "/api/v2/auth/login", nil)
	loginRecorder := httptest.NewRecorder()
	router.ServeHTTP(loginRecorder, loginRequest)
	if loginRecorder.Code != http.StatusOK || loginCalls != 1 {
		t.Fatalf("expected public login route, got status %d and %d calls", loginRecorder.Code, loginCalls)
	}

	unauthorizedRequest := httptest.NewRequest(http.MethodGet, "/api/v2/auth/probe", nil)
	unauthorizedRecorder := httptest.NewRecorder()
	router.ServeHTTP(unauthorizedRecorder, unauthorizedRequest)
	assertErrorResponse(
		t,
		unauthorizedRecorder,
		http.StatusUnauthorized,
		"AUTH_UNAUTHORIZED",
		"unauthorized",
	)

	authorizedRequest := httptest.NewRequest(http.MethodGet, "/api/v2/auth/probe", nil)
	authorizedRequest.Header.Set("Authorization", "Bearer valid-token")
	authorizedRecorder := httptest.NewRecorder()
	router.ServeHTTP(authorizedRecorder, authorizedRequest)
	if authorizedRecorder.Code != http.StatusOK {
		t.Fatalf("expected protected probe status 200, got %d", authorizedRecorder.Code)
	}
	assertJSONEqual(t, authorizedRecorder.Body.Bytes(), []byte(`{"user_id":42}`))
}

func TestNewRouterProtectsAndDispatchesProfileRoutes(t *testing.T) {
	dependencies := validTestDependencies()
	dependencies.Authorization.AuthMiddleware = func(c *gin.Context) {
		if c.GetHeader("Authorization") != "Bearer valid-token" {
			httpx.WriteError(c, http.StatusUnauthorized, "AUTH_UNAUTHORIZED", "unauthorized")
			return
		}
		httpx.SetCurrentUserID(c, 42)
		c.Next()
	}

	getCalls := 0
	dependencies.Accounts.GetProfileHandler = func(c *gin.Context) {
		getCalls++
		userID, _ := httpx.CurrentUserID(c)
		c.JSON(http.StatusOK, gin.H{"user_id": userID})
	}
	patchCalls := 0
	dependencies.Accounts.UpdateProfileHandler = func(c *gin.Context) {
		patchCalls++
		c.Status(http.StatusOK)
	}

	router, err := NewRouter(dependencies)
	if err != nil {
		t.Fatalf("create router: %v", err)
	}

	unauthorizedRequest := httptest.NewRequest(http.MethodGet, "/api/v2/me", nil)
	unauthorizedRecorder := httptest.NewRecorder()
	router.ServeHTTP(unauthorizedRecorder, unauthorizedRequest)
	assertErrorResponse(t, unauthorizedRecorder, http.StatusUnauthorized, "AUTH_UNAUTHORIZED", "unauthorized")

	getRequest := httptest.NewRequest(http.MethodGet, "/api/v2/me", nil)
	getRequest.Header.Set("Authorization", "Bearer valid-token")
	getRecorder := httptest.NewRecorder()
	router.ServeHTTP(getRecorder, getRequest)
	if getRecorder.Code != http.StatusOK || getCalls != 1 {
		t.Fatalf("expected profile GET status 200 and one call, got status %d and %d calls", getRecorder.Code, getCalls)
	}
	assertJSONEqual(t, getRecorder.Body.Bytes(), []byte(`{"user_id":42}`))

	patchRequest := httptest.NewRequest(http.MethodPatch, "/api/v2/me", strings.NewReader(`{"timezone":"UTC"}`))
	patchRequest.Header.Set("Authorization", "Bearer valid-token")
	patchRecorder := httptest.NewRecorder()
	router.ServeHTTP(patchRecorder, patchRequest)
	if patchRecorder.Code != http.StatusOK || patchCalls != 1 {
		t.Fatalf("expected profile PATCH status 200 and one call, got status %d and %d calls", patchRecorder.Code, patchCalls)
	}
}

func TestNewRouterProtectsAndDispatchesSourceAndSubscriptionRoutes(t *testing.T) {
	dependencies := validTestDependencies()
	dependencies.Authorization.AuthMiddleware = func(c *gin.Context) {
		if c.GetHeader("Authorization") != "Bearer valid-token" {
			httpx.WriteError(c, http.StatusUnauthorized, httpx.CodeUnauthorized, "unauthorized")
			return
		}
		httpx.SetCurrentUserID(c, 42)
		c.Next()
	}

	listCalls, getCalls, createCalls := 0, 0, 0
	listSubscriptionCalls, getSubscriptionCalls := 0, 0
	updateSubscriptionCalls, deleteSubscriptionCalls := 0, 0
	listPaperCalls, getPaperCalls := 0, 0
	dependencies.Sources.ListSourcesHandler = func(c *gin.Context) {
		listCalls++
		c.Status(http.StatusOK)
	}
	dependencies.Sources.GetSourceHandler = func(c *gin.Context) {
		getCalls++
		if c.Param("id") != "7" {
			t.Fatalf("expected source ID path parameter 7, got %q", c.Param("id"))
		}
		c.Status(http.StatusOK)
	}
	dependencies.Subscriptions.CreateSubscriptionHandler = func(c *gin.Context) {
		createCalls++
		c.Status(http.StatusCreated)
	}
	dependencies.Subscriptions.ListSubscriptionsHandler = func(c *gin.Context) {
		listSubscriptionCalls++
		c.Status(http.StatusOK)
	}
	dependencies.Subscriptions.GetSubscriptionHandler = func(c *gin.Context) {
		getSubscriptionCalls++
		if c.Param("id") != "9" {
			t.Fatalf("expected subscription ID path parameter 9, got %q", c.Param("id"))
		}
		c.Status(http.StatusOK)
	}
	dependencies.Subscriptions.UpdateSubscriptionHandler = func(c *gin.Context) {
		updateSubscriptionCalls++
		c.Status(http.StatusOK)
	}
	dependencies.Subscriptions.DeleteSubscriptionHandler = func(c *gin.Context) {
		deleteSubscriptionCalls++
		c.Status(http.StatusNoContent)
	}
	dependencies.Papers.ListPapersHandler = func(c *gin.Context) {
		listPaperCalls++
		c.Status(http.StatusOK)
	}
	dependencies.Papers.GetPaperHandler = func(c *gin.Context) {
		getPaperCalls++
		if c.Param("id") != "11" {
			t.Fatalf("expected paper ID path parameter 11, got %q", c.Param("id"))
		}
		c.Status(http.StatusOK)
	}

	router, err := NewRouter(dependencies)
	if err != nil {
		t.Fatalf("create router: %v", err)
	}

	unauthorized := httptest.NewRecorder()
	router.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, "/api/v2/sources", nil))
	assertErrorResponse(t, unauthorized, http.StatusUnauthorized, httpx.CodeUnauthorized, "unauthorized")

	requests := []struct {
		method string
		path   string
		want   int
	}{
		{http.MethodGet, "/api/v2/sources", http.StatusOK},
		{http.MethodGet, "/api/v2/sources/7", http.StatusOK},
		{http.MethodPost, "/api/v2/subscriptions", http.StatusCreated},
		{http.MethodGet, "/api/v2/subscriptions", http.StatusOK},
		{http.MethodGet, "/api/v2/subscriptions/9", http.StatusOK},
		{http.MethodPatch, "/api/v2/subscriptions/9", http.StatusOK},
		{http.MethodDelete, "/api/v2/subscriptions/9", http.StatusNoContent},
		{http.MethodGet, "/api/v2/papers", http.StatusOK},
		{http.MethodGet, "/api/v2/papers/11", http.StatusOK},
	}
	for _, requestCase := range requests {
		request := httptest.NewRequest(requestCase.method, requestCase.path, nil)
		request.Header.Set("Authorization", "Bearer valid-token")
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, request)
		if recorder.Code != requestCase.want {
			t.Fatalf("%s %s: expected %d, got %d", requestCase.method, requestCase.path, requestCase.want, recorder.Code)
		}
	}
	if listCalls != 1 || getCalls != 1 || createCalls != 1 ||
		listSubscriptionCalls != 1 || getSubscriptionCalls != 1 ||
		updateSubscriptionCalls != 1 || deleteSubscriptionCalls != 1 ||
		listPaperCalls != 1 || getPaperCalls != 1 {
		t.Fatalf(
			"unexpected handler calls sources_list=%d source_get=%d create=%d subscriptions_list=%d subscription_get=%d update=%d delete=%d papers_list=%d paper_get=%d",
			listCalls, getCalls, createCalls, listSubscriptionCalls, getSubscriptionCalls,
			updateSubscriptionCalls, deleteSubscriptionCalls, listPaperCalls, getPaperCalls,
		)
	}
}

func TestNewRouterUsesStrictJSONBinding(t *testing.T) {
	router := newTestRouter(t)

	type input struct {
		Name string `json:"name"`
	}

	router.POST("/api/v2/test-json", func(c *gin.Context) {
		var body input
		if !httpx.BindJSON(c, &body) {
			return
		}

		c.JSON(http.StatusOK, body)
	})

	tests := []struct {
		name       string
		body       string
		wantStatus int
	}{
		{
			name:       "valid body",
			body:       `{"name":"alice"}`,
			wantStatus: http.StatusOK,
		},
		{
			name:       "unknown field",
			body:       `{"name":"alice","role":"admin"}`,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "second JSON value",
			body:       `{"name":"alice"} {"name":"bob"}`,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "wrong field type",
			body:       `{"name":123}`,
			wantStatus: http.StatusBadRequest,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(
				http.MethodPost,
				"/api/v2/test-json",
				strings.NewReader(test.body),
			)
			request.Header.Set("Content-Type", "application/json")

			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, request)

			if recorder.Code != test.wantStatus {
				t.Fatalf("expected status %d, got %d", test.wantStatus, recorder.Code)
			}

			assertRequestIDResponseHeader(t, recorder)

			if test.wantStatus == http.StatusBadRequest {
				assertErrorResponse(
					t,
					recorder,
					http.StatusBadRequest,
					httpx.CodeValidationError,
					"request is invalid",
				)
			}
		})
	}
}

func TestNewRouterUsesReleaseModeInProduction(t *testing.T) {
	previousMode := gin.Mode()
	t.Cleanup(func() {
		gin.SetMode(previousMode)
	})

	dependencies := validTestDependencies()
	dependencies.AppEnv = "production"

	if _, err := NewRouter(dependencies); err != nil {
		t.Fatalf("create router: %v", err)
	}

	if gin.Mode() != gin.ReleaseMode {
		t.Fatalf("expected Gin release mode, got %q", gin.Mode())
	}
}

func TestNewRouterDoesNotTrustForwardedClientIP(t *testing.T) {
	router := newTestRouter(t)

	router.GET("/client-ip", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"client_ip": c.ClientIP()})
	})

	request := httptest.NewRequest(http.MethodGet, "/client-ip", nil)
	request.RemoteAddr = "192.0.2.10:4321"
	request.Header.Set("X-Forwarded-For", "203.0.113.20")

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)

	var response struct {
		ClientIP string `json:"client_ip"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if response.ClientIP != "192.0.2.10" {
		t.Fatalf(
			"expected remote IP 192.0.2.10, got %q",
			response.ClientIP,
		)
	}
}

func TestNewRouterValidatesDependencies(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Dependencies)
	}{
		{
			name: "missing app environment",
			mutate: func(dependencies *Dependencies) {
				dependencies.AppEnv = ""
			},
		},
		{
			name: "invalid app environment",
			mutate: func(dependencies *Dependencies) {
				dependencies.AppEnv = "staging"
			},
		},
		{
			name: "missing service name",
			mutate: func(dependencies *Dependencies) {
				dependencies.ServiceName = ""
			},
		},
		{
			name: "missing logger",
			mutate: func(dependencies *Dependencies) {
				dependencies.Logger = nil
			},
		},
		{
			name: "missing MySQL check",
			mutate: func(dependencies *Dependencies) {
				dependencies.MySQLCheck = nil
			},
		},
		{
			name: "missing Redis check",
			mutate: func(dependencies *Dependencies) {
				dependencies.RedisCheck = nil
			},
		},
		{
			name: "missing register handler",
			mutate: func(dependencies *Dependencies) {
				dependencies.Accounts.RegisterHandler = nil
			},
		},
		{
			name: "missing login handler",
			mutate: func(dependencies *Dependencies) {
				dependencies.Accounts.LoginHandler = nil
			},
		},
		{
			name: "missing auth middleware",
			mutate: func(dependencies *Dependencies) {
				dependencies.Authorization.AuthMiddleware = nil
			},
		},
		{
			name:   "missing active account middleware",
			mutate: func(dependencies *Dependencies) { dependencies.Authorization.ActiveAccountMiddleware = nil },
		},
		{
			name: "missing get profile handler",
			mutate: func(dependencies *Dependencies) {
				dependencies.Accounts.GetProfileHandler = nil
			},
		},
		{
			name: "missing update profile handler",
			mutate: func(dependencies *Dependencies) {
				dependencies.Accounts.UpdateProfileHandler = nil
			},
		},
		{
			name: "missing list sources handler",
			mutate: func(dependencies *Dependencies) {
				dependencies.Sources.ListSourcesHandler = nil
			},
		},
		{
			name: "missing get source handler",
			mutate: func(dependencies *Dependencies) {
				dependencies.Sources.GetSourceHandler = nil
			},
		},
		{
			name: "missing create subscription handler",
			mutate: func(dependencies *Dependencies) {
				dependencies.Subscriptions.CreateSubscriptionHandler = nil
			},
		},
		{
			name: "missing list subscriptions handler",
			mutate: func(dependencies *Dependencies) {
				dependencies.Subscriptions.ListSubscriptionsHandler = nil
			},
		},
		{
			name: "missing get subscription handler",
			mutate: func(dependencies *Dependencies) {
				dependencies.Subscriptions.GetSubscriptionHandler = nil
			},
		},
		{
			name: "missing update subscription handler",
			mutate: func(dependencies *Dependencies) {
				dependencies.Subscriptions.UpdateSubscriptionHandler = nil
			},
		},
		{
			name: "missing delete subscription handler",
			mutate: func(dependencies *Dependencies) {
				dependencies.Subscriptions.DeleteSubscriptionHandler = nil
			},
		},
		{
			name: "missing list papers handler",
			mutate: func(dependencies *Dependencies) {
				dependencies.Papers.ListPapersHandler = nil
			},
		},
		{
			name: "missing get paper handler",
			mutate: func(dependencies *Dependencies) {
				dependencies.Papers.GetPaperHandler = nil
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dependencies := validTestDependencies()
			test.mutate(&dependencies)

			if _, err := NewRouter(dependencies); err == nil {
				t.Fatal("expected dependency validation error")
			}
		})
	}
}

func newTestRouter(t *testing.T) *gin.Engine {
	t.Helper()

	router, err := NewRouter(validTestDependencies())
	if err != nil {
		t.Fatalf("create test router: %v", err)
	}

	return router
}

func validTestDependencies() Dependencies {
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))

	return Dependencies{
		AppEnv:      "test",
		ServiceName: testServiceName,
		Logger:      logger,
		MySQLCheck: func(context.Context) error {
			return nil
		},
		RedisCheck: func(context.Context) error {
			return nil
		}, Accounts: AccountsRoutes{RefreshHandler: func(c *gin.Context) { c.Status(200) }, LogoutHandler: func(c *gin.Context) { c.Status(204) }, RegisterHandler: func(c *gin.Context) {
			c.Status(http.StatusCreated)
		},
			LoginHandler: func(c *gin.Context) {
				c.Status(http.StatusOK)
			},

			GetProfileHandler: func(c *gin.Context) {
				c.Status(http.StatusOK)
			},
			UpdateProfileHandler: func(c *gin.Context) {
				c.Status(http.StatusOK)
			}}, Authorization: AuthorizationRoutes{AuthMiddleware: func(c *gin.Context) {
			c.Next()
		},
			ActiveAccountMiddleware: func(c *gin.Context) { c.Next() }}, Sources: SourcesRoutes{ListSourcesHandler: func(c *gin.Context) {
			c.Status(http.StatusOK)
		},
			GetSourceHandler: func(c *gin.Context) {
				c.Status(http.StatusOK)
			}}, Subscriptions: SubscriptionsRoutes{CreateSubscriptionHandler: func(c *gin.Context) {
			c.Status(http.StatusCreated)
		},
			ListSubscriptionsHandler: func(c *gin.Context) {
				c.Status(http.StatusOK)
			},
			GetSubscriptionHandler: func(c *gin.Context) {
				c.Status(http.StatusOK)
			},
			UpdateSubscriptionHandler: func(c *gin.Context) {
				c.Status(http.StatusOK)
			},
			DeleteSubscriptionHandler: func(c *gin.Context) {
				c.Status(http.StatusNoContent)
			}}, Papers: PapersRoutes{ListPapersHandler: func(c *gin.Context) {
			c.Status(http.StatusOK)
		},
			GetPaperHandler: func(c *gin.Context) {
				c.Status(http.StatusOK)
			}},
	}
}

func assertErrorResponse(
	t *testing.T,
	recorder *httptest.ResponseRecorder,
	wantStatus int,
	wantCode string,
	wantMessage string,
) {
	t.Helper()

	if recorder.Code != wantStatus {
		t.Fatalf("expected status %d, got %d", wantStatus, recorder.Code)
	}

	var response httpx.ErrorResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode error response: %v", err)
	}

	if response.Code != wantCode {
		t.Fatalf("expected error code %q, got %q", wantCode, response.Code)
	}

	if response.Message != wantMessage {
		t.Fatalf("expected message %q, got %q", wantMessage, response.Message)
	}

	responseID := assertRequestIDResponseHeader(t, recorder)
	if response.RequestID != responseID {
		t.Fatalf(
			"expected response request ID %q, got %q",
			responseID,
			response.RequestID,
		)
	}
}

func assertRequestIDResponseHeader(
	t *testing.T,
	recorder *httptest.ResponseRecorder,
) string {
	t.Helper()

	requestID := recorder.Header().Get(httpx.HeaderRequestID)
	if requestID == "" {
		t.Fatal("expected X-Request-ID response header")
	}

	return requestID
}

func assertJSONEqual(t *testing.T, got []byte, want []byte) {
	t.Helper()

	var gotJSON any
	if err := json.Unmarshal(got, &gotJSON); err != nil {
		t.Fatalf("decode actual JSON: %v", err)
	}

	var wantJSON any
	if err := json.Unmarshal(want, &wantJSON); err != nil {
		t.Fatalf("decode expected JSON: %v", err)
	}

	gotEncoded, err := json.Marshal(gotJSON)
	if err != nil {
		t.Fatalf("encode actual JSON: %v", err)
	}

	wantEncoded, err := json.Marshal(wantJSON)
	if err != nil {
		t.Fatalf("encode expected JSON: %v", err)
	}

	if string(gotEncoded) != string(wantEncoded) {
		t.Fatalf("expected JSON %s, got %s", wantEncoded, gotEncoded)
	}
}
