package user

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"signalwatch/internal/platform/httpx"
)

const profilePath = "/api/v2/me"

func TestHandlerGetProfileReturnsDefaultPublicProfile(t *testing.T) {
	want := profileTestUser()
	service := registrationServiceStub{
		register: func(context.Context, RegisterInput) (User, error) { return User{}, nil },
		getProfile: func(_ context.Context, userID uint64) (User, error) {
			if userID != want.ID {
				t.Fatalf("expected user ID %d, got %d", want.ID, userID)
			}
			return want, nil
		},
	}
	router := newProfileHandlerTestRouter(service, true)

	request := httptest.NewRequest(http.MethodGet, profilePath, nil)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", recorder.Code)
	}
	var response PublicUser
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response != want.Public() {
		t.Fatalf("expected profile %+v, got %+v", want.Public(), response)
	}
	for _, secret := range []string{want.PasswordHash, "password_hash"} {
		if strings.Contains(recorder.Body.String(), secret) {
			t.Fatalf("response leaked sensitive value %q", secret)
		}
	}
}

func TestHandlerUpdateProfileReturnsNormalizedLatestProfile(t *testing.T) {
	want := profileTestUser()
	want.Timezone = "Asia/Shanghai"
	want.DigestTime = "08:30:00"
	want.MaxItemsPerDigest = 30

	service := registrationServiceStub{
		register: func(context.Context, RegisterInput) (User, error) { return User{}, nil },
		updateProfile: func(_ context.Context, userID uint64, input UpdateProfileInput) (User, error) {
			if userID != want.ID {
				t.Fatalf("expected user ID %d, got %d", want.ID, userID)
			}
			if input.Timezone == nil || *input.Timezone != "Asia/Shanghai" ||
				input.DigestTime == nil || *input.DigestTime != "08:30" ||
				input.MaxItemsPerDigest == nil || *input.MaxItemsPerDigest != 30 {
				t.Fatalf("unexpected update input %+v", input)
			}
			return want, nil
		},
	}
	router := newProfileHandlerTestRouter(service, true)
	request := httptest.NewRequest(
		http.MethodPatch,
		profilePath,
		strings.NewReader(`{"timezone":"Asia/Shanghai","digest_time":"08:30","max_items_per_digest":30}`),
	)
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", recorder.Code, recorder.Body.String())
	}
	var response PublicUser
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response != want.Public() {
		t.Fatalf("expected profile %+v, got %+v", want.Public(), response)
	}
	if response.DigestTime != "08:30" {
		t.Fatalf("expected API digest time 08:30, got %q", response.DigestTime)
	}
	if strings.Contains(recorder.Body.String(), "password") {
		t.Fatal("response leaked password data")
	}
}

func TestHandlerUpdateProfileRejectsInvalidRequestsWithoutPersistence(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "empty patch", body: `{}`},
		{name: "invalid timezone", body: `{"timezone":"Asia/NotExist"}`},
		{name: "local timezone", body: `{"timezone":"Local"}`},
		{name: "invalid time", body: `{"digest_time":"8:30"}`},
		{name: "zero max items", body: `{"max_items_per_digest":0}`},
		{name: "max items above limit", body: `{"max_items_per_digest":51}`},
		{name: "unknown email field", body: `{"email":"other@example.com"}`},
		{name: "unknown password field", body: `{"password":"replacement"}`},
		{name: "unknown status field", body: `{"status":"disabled"}`},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			updateCalls := 0
			repository := repositoryStub{
				updateProfile: func(context.Context, uint64, ProfileChanges) (User, error) {
					updateCalls++
					return profileTestUser(), nil
				},
			}
			router := newProfileHandlerTestRouter(NewService(repository), true)
			request := httptest.NewRequest(http.MethodPatch, profilePath, strings.NewReader(test.body))
			request.Header.Set("Content-Type", "application/json")
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, request)

			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("expected status 400, got %d: %s", recorder.Code, recorder.Body.String())
			}
			if updateCalls != 0 {
				t.Fatalf("expected database values unchanged, got %d update calls", updateCalls)
			}
		})
	}
}

func TestHandlerProfileReturnsUnauthorizedWithoutCurrentActiveUser(t *testing.T) {
	tests := []struct {
		name     string
		method   string
		body     string
		withUser bool
		service  UserService
	}{
		{
			name:     "missing authenticated context",
			method:   http.MethodGet,
			withUser: false,
			service: registrationServiceStub{
				register: func(context.Context, RegisterInput) (User, error) { return User{}, nil },
			},
		},
		{
			name:     "get user missing or inactive",
			method:   http.MethodGet,
			withUser: true,
			service: registrationServiceStub{
				register: func(context.Context, RegisterInput) (User, error) { return User{}, nil },
				getProfile: func(context.Context, uint64) (User, error) {
					return User{}, ErrNotFound
				},
			},
		},
		{
			name:     "update user missing or inactive",
			method:   http.MethodPatch,
			body:     `{"timezone":"UTC"}`,
			withUser: true,
			service: registrationServiceStub{
				register: func(context.Context, RegisterInput) (User, error) { return User{}, nil },
				updateProfile: func(context.Context, uint64, UpdateProfileInput) (User, error) {
					return User{}, ErrNotFound
				},
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			router := newProfileHandlerTestRouter(test.service, test.withUser)
			request := httptest.NewRequest(test.method, profilePath, strings.NewReader(test.body))
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, request)
			assertHandlerErrorResponse(t, recorder, http.StatusUnauthorized, httpx.CodeUnauthorized, "unauthorized")
		})
	}
}

func newProfileHandlerTestRouter(service UserService, withUser bool) *gin.Engine {
	gin.SetMode(gin.TestMode)
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	handler := NewHandler(service, logger)
	router := gin.New()
	router.Use(httpx.RequestIDMiddleware())
	if withUser {
		router.Use(func(c *gin.Context) {
			httpx.SetCurrentUserID(c, 42)
			c.Next()
		})
	}
	router.GET(profilePath, handler.GetProfile)
	router.PATCH(profilePath, handler.UpdateProfile)
	return router
}
