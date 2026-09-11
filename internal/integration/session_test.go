package integration_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"signalwatch/internal/auth"
	"signalwatch/internal/user"
)

func TestMySQLBrowserSessionSurvivesIdleRestartAndConcurrentRefresh(t *testing.T) {
	db, sqlDB, _ := openM1TestDatabase(t)
	t.Cleanup(func() { _ = sqlDB.Close() })
	now := time.Now().UTC().Truncate(time.Second)
	clock := func() time.Time { return now }
	var logs bytes.Buffer
	api := newM1TestAPIWithClock(t, db, sqlDB.PingContext, &logs, clock)
	email := fmt.Sprintf("session-%d@example.test", time.Now().UnixNano())
	t.Cleanup(func() {
		if err := db.Where("email=?", email).Delete(&user.User{}).Error; err != nil {
			t.Error(err)
		}
	})
	api.do(t, "POST", "/api/v2/auth/register", "", "", map[string]any{"email": email, "password": testPassword}, 201)
	call := func(target testAPI, path string, cookie *http.Cookie, body []byte, status int) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest("POST", path, bytes.NewReader(body))
		req.Header.Set(auth.SessionRequestHeader, "1")
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		if cookie != nil {
			req.AddCookie(cookie)
		}
		r := httptest.NewRecorder()
		target.router.ServeHTTP(r, req)
		if r.Code != status {
			t.Fatalf("%s: status=%d expected=%d body=%s", path, r.Code, status, r.Body.String())
		}
		validateWireContract(t, req, body, r)
		return r
	}
	body, _ := json.Marshal(map[string]string{"email": email, "password": testPassword})
	login := call(api, "/api/v2/auth/login", nil, body, 200)
	if len(login.Result().Cookies()) != 1 {
		t.Fatal("login cookie missing")
	}
	cookie := login.Result().Cookies()[0]
	if !cookie.HttpOnly || cookie.Path != "/api/v2/auth" || !cookie.Expires.Equal(now.Add(7*24*time.Hour)) {
		t.Fatal("cookie lifetime or policy incorrect")
	}
	var initial loginResponse
	if err := json.Unmarshal(login.Body.Bytes(), &initial); err != nil {
		t.Fatal(err)
	}
	var hashes []string
	if err := db.Table("auth_sessions").Where("user_id=(SELECT id FROM users WHERE email=?)", email).Pluck("token_hash", &hashes).Error; err != nil {
		t.Fatal(err)
	}
	if len(hashes) != 1 || hashes[0] == cookie.Value || len(hashes[0]) != 64 {
		t.Fatal("session secret must be hashed in database")
	}
	now = now.Add(31 * time.Minute)
	api.do(t, "GET", "/api/v2/me", initial.AccessToken, "", nil, 401)
	// A newly assembled API instance recovers the login from MySQL, not process memory.
	restarted := newM1TestAPIWithClock(t, db, sqlDB.PingContext, &logs, clock)
	renewed := call(restarted, "/api/v2/auth/refresh", cookie, nil, 200)
	if len(renewed.Result().Cookies()) != 0 {
		t.Fatal("refresh must not extend or rotate the login cookie")
	}
	var fresh loginResponse
	if err := json.Unmarshal(renewed.Body.Bytes(), &fresh); err != nil {
		t.Fatal(err)
	}
	restarted.do(t, "GET", "/api/v2/me", fresh.AccessToken, "", nil, 200)
	// Separate log buffers avoid introducing a test-only data race in access logs.
	var wg sync.WaitGroup
	statuses := make(chan int, 8)
	for range 8 {
		var localLogs bytes.Buffer
		instance := newM1TestAPIWithClock(t, db, sqlDB.PingContext, &localLogs, clock)
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := httptest.NewRequest("POST", "/api/v2/auth/refresh", nil)
			req.Header.Set(auth.SessionRequestHeader, "1")
			req.AddCookie(cookie)
			r := httptest.NewRecorder()
			instance.router.ServeHTTP(r, req)
			statuses <- r.Code
		}()
	}
	wg.Wait()
	close(statuses)
	for status := range statuses {
		if status != 200 {
			t.Fatalf("concurrent refresh returned %d", status)
		}
	}
	second := call(api, "/api/v2/auth/login", nil, body, 200).Result().Cookies()[0]
	call(api, "/api/v2/auth/logout", cookie, nil, 204)
	call(api, "/api/v2/auth/refresh", cookie, nil, 401)
	call(api, "/api/v2/auth/refresh", second, nil, 200)
	// Seven days is an absolute lifetime, not seven days from the last refresh.
	now = second.Expires
	call(api, "/api/v2/auth/refresh", second, nil, 401)
	if strings.Contains(logs.String(), cookie.Value) || strings.Contains(login.Body.String(), cookie.Value) {
		t.Fatal("cookie secret leaked outside Set-Cookie")
	}
	// Disabled accounts cannot use an otherwise valid renewal session.
	third := call(api, "/api/v2/auth/login", nil, body, 200).Result().Cookies()[0]
	if err := db.Model(&user.User{}).Where("email=?", email).Update("status", "disabled").Error; err != nil {
		t.Fatal(err)
	}
	call(api, "/api/v2/auth/refresh", third, nil, 401)
}
