package user

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"signalwatch/internal/platform/httpx"
)

const registerPath = "/api/v2/auth/register"

func TestHandlerRegisterReturnsCreatedPublicUser(t *testing.T) {
	createdAt := time.Date(2026, time.September, 3, 8, 0, 0, 0, time.UTC)
	updatedAt := createdAt.Add(time.Second)
	wantInput := RegisterInput{
		Email:    "Alice@Example.com",
		Password: "correct-horse-123",
	}
	createdUser := User{
		ID:                42,
		Email:             "alice@example.com",
		PasswordHash:      "$2a$10$PASSWORD_HASH_MUST_NOT_LEAK",
		Timezone:          DefaultTimezone,
		DigestTime:        DefaultDigestTime,
		MaxItemsPerDigest: DefaultMaxItemsPerDigest,
		Status:            StatusActive,
		Role:              RoleUser,
		CreatedAt:         createdAt,
		UpdatedAt:         updatedAt,
	}

	serviceCalls := 0
	var gotInput RegisterInput
	service := registrationServiceStub{
		register: func(_ context.Context, input RegisterInput) (User, error) {
			serviceCalls++
			gotInput = input
			return createdUser, nil
		},
	}
	logger, logs := newHandlerTestLogger()
	router := newHandlerTestRouter(service, logger)

	request := httptest.NewRequest(
		http.MethodPost,
		registerPath,
		strings.NewReader(`{"email":"Alice@Example.com","password":"correct-horse-123"}`),
	)
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()

	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusCreated {
		t.Fatalf("expected status %d, got %d", http.StatusCreated, recorder.Code)
	}
	if serviceCalls != 1 {
		t.Fatalf("expected service Register to be called once, got %d", serviceCalls)
	}
	if gotInput != wantInput {
		t.Fatalf("expected service input %+v, got %+v", wantInput, gotInput)
	}

	var response PublicUser
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if want := createdUser.Public(); response != want {
		t.Fatalf("expected response %+v, got %+v", want, response)
	}

	responseBody := recorder.Body.String()
	for _, secret := range []string{
		wantInput.Password,
		createdUser.PasswordHash,
		"password_hash",
		`"role"`,
	} {
		if strings.Contains(responseBody, secret) {
			t.Fatalf("response leaked sensitive value %q", secret)
		}
	}
	if logs.Len() != 0 {
		t.Fatalf("expected no error log for successful registration, got %s", logs.String())
	}
	assertHandlerRequestID(t, recorder)
}

func TestHandlerRegisterRejectsUnknownJSONField(t *testing.T) {
	serviceCalls := 0
	service := registrationServiceStub{
		register: func(context.Context, RegisterInput) (User, error) {
			serviceCalls++
			return User{}, nil
		},
	}
	logger, logs := newHandlerTestLogger()
	router := newHandlerTestRouter(service, logger)

	request := httptest.NewRequest(
		http.MethodPost,
		registerPath,
		strings.NewReader(
			`{"email":"alice@example.com","password":"correct-horse-123","role":"admin"}`,
		),
	)
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()

	router.ServeHTTP(recorder, request)

	assertHandlerErrorResponse(
		t,
		recorder,
		http.StatusBadRequest,
		httpx.CodeValidationError,
		"request is invalid",
	)
	if serviceCalls != 0 {
		t.Fatalf("expected service not to be called, got %d calls", serviceCalls)
	}
	if logs.Len() != 0 {
		t.Fatalf("expected no error log for invalid JSON, got %s", logs.String())
	}
}

func TestHandlerRegisterMapsServiceErrors(t *testing.T) {
	const (
		password       = "correct-horse-123"
		internalReason = "DATABASE_INTERNAL_REASON"
	)

	tests := []struct {
		name        string
		serviceErr  error
		wantStatus  int
		wantCode    string
		wantMessage string
		wantLog     bool
	}{
		{
			name:        "invalid email",
			serviceErr:  ErrInvalidEmail,
			wantStatus:  http.StatusBadRequest,
			wantCode:    httpx.CodeValidationError,
			wantMessage: "email is invalid",
		},
		{
			name:        "invalid password",
			serviceErr:  ErrInvalidPassword,
			wantStatus:  http.StatusBadRequest,
			wantCode:    httpx.CodeValidationError,
			wantMessage: "password is invalid",
		},
		{
			name: "email already registered",
			serviceErr: fmt.Errorf(
				"create user: %w",
				ErrEmailAlreadyRegistered,
			),
			wantStatus:  http.StatusConflict,
			wantCode:    CodeEmailAlreadyRegistered,
			wantMessage: "email is already registered",
		},
		{
			name:        "unexpected service error",
			serviceErr:  errors.New(internalReason),
			wantStatus:  http.StatusInternalServerError,
			wantCode:    httpx.CodeInternalError,
			wantMessage: "internal server error",
			wantLog:     true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := registrationServiceStub{
				register: func(context.Context, RegisterInput) (User, error) {
					return User{}, test.serviceErr
				},
			}
			logger, logs := newHandlerTestLogger()
			router := newHandlerTestRouter(service, logger)
			request := httptest.NewRequest(
				http.MethodPost,
				registerPath,
				strings.NewReader(
					`{"email":"alice@example.com","password":"`+password+`"}`,
				),
			)
			request.Header.Set("Content-Type", "application/json")
			recorder := httptest.NewRecorder()

			router.ServeHTTP(recorder, request)

			assertHandlerErrorResponse(
				t,
				recorder,
				test.wantStatus,
				test.wantCode,
				test.wantMessage,
			)

			responseBody := recorder.Body.String()
			if strings.Contains(responseBody, internalReason) {
				t.Fatal("response leaked the internal error reason")
			}
			if strings.Contains(logs.String(), password) {
				t.Fatal("error log leaked the request password")
			}

			if test.wantLog {
				if !strings.Contains(logs.String(), internalReason) {
					t.Fatalf("expected internal error log, got %s", logs.String())
				}
				return
			}
			if logs.Len() != 0 {
				t.Fatalf("expected no error log, got %s", logs.String())
			}
		})
	}
}

type registrationServiceStub struct {
	register      func(context.Context, RegisterInput) (User, error)
	getProfile    func(context.Context, uint64) (User, error)
	updateProfile func(context.Context, uint64, UpdateProfileInput) (User, error)
}

func (stub registrationServiceStub) GetProfile(ctx context.Context, userID uint64) (User, error) {
	if stub.getProfile == nil {
		return User{}, ErrNotFound
	}
	return stub.getProfile(ctx, userID)
}

func (stub registrationServiceStub) UpdateProfile(
	ctx context.Context,
	userID uint64,
	input UpdateProfileInput,
) (User, error) {
	if stub.updateProfile == nil {
		return User{}, ErrNotFound
	}
	return stub.updateProfile(ctx, userID, input)
}

func (stub registrationServiceStub) Register(
	ctx context.Context,
	input RegisterInput,
) (User, error) {
	return stub.register(ctx, input)
}

func newHandlerTestRouter(
	service UserService,
	logger *slog.Logger,
) *gin.Engine {
	gin.SetMode(gin.TestMode)

	handler := NewHandler(service, logger)
	router := gin.New()
	router.Use(httpx.RequestIDMiddleware())
	router.POST(registerPath, handler.Register)

	return router
}

func newHandlerTestLogger() (*slog.Logger, *bytes.Buffer) {
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, nil))

	return logger, &output
}

func assertHandlerErrorResponse(
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
		t.Fatalf("expected code %q, got %q", wantCode, response.Code)
	}
	if response.Message != wantMessage {
		t.Fatalf("expected message %q, got %q", wantMessage, response.Message)
	}

	requestID := assertHandlerRequestID(t, recorder)
	if response.RequestID != requestID {
		t.Fatalf(
			"expected response request ID %q, got %q",
			requestID,
			response.RequestID,
		)
	}
}

func assertHandlerRequestID(t *testing.T, recorder *httptest.ResponseRecorder) string {
	t.Helper()

	requestID := recorder.Header().Get(httpx.HeaderRequestID)
	if requestID == "" {
		t.Fatal("expected X-Request-ID response header")
	}

	return requestID
}
