package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"signalwatch/internal/platform/httpx"
)

type memorySessions struct {
	sessions    map[string]Session
	unavailable bool
	active      bool
}

func (m *memorySessions) Create(_ context.Context, s Session) error {
	if m.unavailable {
		return errors.New("database unavailable")
	}
	m.sessions[s.TokenHash] = s
	return nil
}
func (m *memorySessions) FindActive(_ context.Context, h string, now time.Time) (Session, error) {
	if m.unavailable {
		return Session{}, errors.New("database unavailable")
	}
	s, ok := m.sessions[h]
	if !ok || !m.active || !s.ExpiresAt.After(now) {
		return Session{}, ErrInvalidSession
	}
	return s, nil
}
func (m *memorySessions) Delete(_ context.Context, h string) error {
	if m.unavailable {
		return errors.New("database unavailable")
	}
	delete(m.sessions, h)
	return nil
}

func TestSessionRenewsAfterHalfHourWithoutExtendingSevenDayLifetime(t *testing.T) {
	now := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	tokens := newTestTokenService(t, testJWTSecret, "signalwatch-api", 15*time.Minute, func() time.Time { return now })
	store := &memorySessions{sessions: map[string]Session{}, active: true}
	sessions, err := NewSessionService(store, tokens, 7*24*time.Hour, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	login, err := sessions.IssueSession(t.Context(), 42)
	if err != nil {
		t.Fatal(err)
	}
	if !validSessionSecret(login.RefreshToken) {
		t.Fatal("invalid cookie secret")
	}
	if _, ok := store.sessions[login.RefreshToken]; ok {
		t.Fatal("raw secret persisted")
	}
	if _, ok := store.sessions[sessionHash(login.RefreshToken)]; !ok {
		t.Fatal("hash not persisted")
	}
	now = now.Add(31 * time.Minute)
	if _, err = tokens.Verify(login.AccessToken); !errors.Is(err, ErrInvalidToken) {
		t.Fatal("old access token should expire")
	}
	refreshed, err := sessions.Refresh(t.Context(), login.RefreshToken)
	if err != nil {
		t.Fatal(err)
	}
	if uid, err := tokens.Verify(refreshed.AccessToken); err != nil || uid != 42 {
		t.Fatalf("renewed access: uid=%d err=%v", uid, err)
	}
	now = login.SessionExpiresAt.Add(-30 * time.Second)
	final, err := sessions.Refresh(t.Context(), login.RefreshToken)
	if err != nil || final.ExpiresIn != 30 {
		t.Fatalf("final access must be capped: %d %v", final.ExpiresIn, err)
	}
	now = login.SessionExpiresAt
	if _, err = sessions.Refresh(t.Context(), login.RefreshToken); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("seven-day expiry: %v", err)
	}
	if _, err = tokens.Verify(final.AccessToken); !errors.Is(err, ErrInvalidToken) {
		t.Fatal("access outlived login session")
	}
}
func TestSessionRevocationDisabledAccountAndTransientFailure(t *testing.T) {
	now := time.Now().UTC()
	clock := func() time.Time { return now }
	tokens := newTestTokenService(t, testJWTSecret, "signalwatch-api", 15*time.Minute, clock)
	store := &memorySessions{sessions: map[string]Session{}, active: true}
	service, err := NewSessionService(store, tokens, 168*time.Hour, clock)
	if err != nil {
		t.Fatal(err)
	}
	login, err := service.IssueSession(t.Context(), 42)
	if err != nil {
		t.Fatal(err)
	}
	store.unavailable = true
	if _, err = service.Refresh(t.Context(), login.RefreshToken); err == nil || errors.Is(err, ErrInvalidSession) {
		t.Fatal("storage failure misclassified as expired")
	}
	store.unavailable = false
	store.active = false
	if _, err = service.Refresh(t.Context(), login.RefreshToken); !errors.Is(err, ErrInvalidSession) {
		t.Fatal("disabled account renewed")
	}
	store.active = true
	if err = service.Logout(t.Context(), login.RefreshToken); err != nil {
		t.Fatal(err)
	}
	if err = service.Logout(t.Context(), login.RefreshToken); err != nil {
		t.Fatal(err)
	}
	if _, err = service.Refresh(t.Context(), login.RefreshToken); !errors.Is(err, ErrInvalidSession) {
		t.Fatal("revoked session renewed")
	}
	for _, raw := range []string{"", "bad", strings.Repeat("x", 1024)} {
		if _, err = service.Refresh(t.Context(), raw); !errors.Is(err, ErrInvalidSession) {
			t.Fatalf("accepted invalid secret")
		}
	}
}

type browserSessionsStub struct {
	refresh func(context.Context, string) (IssuedToken, error)
	logout  func(context.Context, string) error
}

func (s browserSessionsStub) Refresh(ctx context.Context, raw string) (IssuedToken, error) {
	if s.refresh != nil {
		return s.refresh(ctx, raw)
	}
	return IssuedToken{}, ErrInvalidSession
}
func (s browserSessionsStub) Logout(ctx context.Context, raw string) error {
	if s.logout != nil {
		return s.logout(ctx, raw)
	}
	return nil
}
func TestBrowserSessionHTTPBoundary(t *testing.T) {
	gin.SetMode(gin.TestMode)
	logger, _ := newAuthHandlerTestLogger()
	expires := time.Now().Add(168 * time.Hour).UTC().Truncate(time.Second)
	handler := NewHandler(loginServiceStub{login: func(context.Context, LoginInput) (LoginResult, error) {
		return LoginResult{AccessToken: "short-token", ExpiresIn: 900, RefreshToken: "private-cookie-secret", SessionExpiresAt: expires}, nil
	}}, browserSessionsStub{}, true, logger)
	router := gin.New()
	router.Use(httpx.RequestIDMiddleware())
	router.POST(loginPath, handler.Login)
	router.POST("/refresh", handler.Refresh)
	router.POST("/logout", handler.Logout)
	req := httptest.NewRequest("POST", loginPath, strings.NewReader(`{"email":"reader@example.test","password":"password-123"}`))
	req.Header.Set("Content-Type", "application/json")
	r := httptest.NewRecorder()
	router.ServeHTTP(r, req)
	if r.Code != 200 || strings.Contains(r.Body.String(), "private-cookie-secret") {
		t.Fatal("login response leaked or failed")
	}
	cookies := r.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatal("missing login cookie")
	}
	c := cookies[0]
	if !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteLaxMode || c.Path != "/api/v2/auth" || c.Domain != "" || !c.Expires.Equal(expires) {
		t.Fatalf("cookie policy: %+v", c)
	}
	if r.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("token response may be cached")
	}
	for _, path := range []string{"/refresh", "/logout"} {
		req = httptest.NewRequest("POST", path, nil)
		r = httptest.NewRecorder()
		router.ServeHTTP(r, req)
		if r.Code != 403 {
			t.Fatal("missing CSRF header accepted")
		}
		req.Header.Set(SessionRequestHeader, "1")
		req.Header.Set("Sec-Fetch-Site", "cross-site")
		r = httptest.NewRecorder()
		router.ServeHTTP(r, req)
		if r.Code != 403 {
			t.Fatal("cross-site request accepted")
		}
	}
	req = httptest.NewRequest("POST", "/refresh", nil)
	req.Header.Set(SessionRequestHeader, "1")
	r = httptest.NewRecorder()
	router.ServeHTTP(r, req)
	if r.Code != 401 || len(r.Result().Cookies()) != 0 {
		t.Fatal("rejected refresh must not overwrite a newer login cookie")
	}
}
func TestBrowserSessionStorageFailureRetainsCookieForRetry(t *testing.T) {
	logger, _ := newAuthHandlerTestLogger()
	unavailable := errors.New("database unavailable")
	handler := NewHandler(loginServiceStub{}, browserSessionsStub{refresh: func(context.Context, string) (IssuedToken, error) { return IssuedToken{}, unavailable }, logout: func(context.Context, string) error { return unavailable }}, false, logger)
	router := gin.New()
	router.Use(httpx.RequestIDMiddleware())
	router.POST("/refresh", handler.Refresh)
	router.POST("/logout", handler.Logout)
	for _, path := range []string{"/refresh", "/logout"} {
		req := httptest.NewRequest("POST", path, nil)
		req.Header.Set(SessionRequestHeader, "1")
		r := httptest.NewRecorder()
		router.ServeHTTP(r, req)
		if r.Code != 503 || len(r.Result().Cookies()) != 0 {
			t.Fatal("transient failure destroyed cookie")
		}
	}
}
