package integration_test

import (
	"bytes"
	"fmt"
	"net/http"
	"signalwatch/internal/source"
	"testing"
	"time"
)

func TestSubscriptionDigestLimitAPIInheritanceAndIsolation(t *testing.T) {
	db, sqlDB, _ := openM1TestDatabase(t)
	t.Cleanup(func() { sqlDB.Close() })
	email := fmt.Sprintf("sub-limit-%d@example.test", time.Now().UnixNano())
	t.Cleanup(func() { cleanupM1TestData(t, db, []string{email}, "") })
	var logs bytes.Buffer
	api := newM1TestAPI(t, db, sqlDB.PingContext, &logs)
	api.do(t, http.MethodPost, "/api/v2/auth/register", "", "", map[string]any{"email": email, "password": testPassword}, 201)
	login := decodeResponse[loginResponse](t, api.do(t, http.MethodPost, "/api/v2/auth/login", "", "", map[string]any{"email": email, "password": testPassword}, 200))
	token := login.AccessToken
	api.do(t, http.MethodPatch, "/api/v2/me", token, "", map[string]any{"max_items_per_digest": 10}, 200)
	var src source.Source
	if err := db.Where("source_key=?", "arxiv").Take(&src).Error; err != nil {
		t.Fatal(err)
	}
	type response struct {
		ID      uint64 `json:"id"`
		Max     uint16 `json:"max_items_per_digest"`
		Version uint32 `json:"version"`
	}
	create := func(name string, limit any, status int) response {
		body := map[string]any{"source_id": src.ID, "name": name, "rules": map[string]any{"category": "cs.AI"}}
		if limit != nil {
			body["max_items_per_digest"] = limit
		}
		r := api.do(t, http.MethodPost, "/api/v2/subscriptions", token, "", body, status)
		if status != 201 {
			return response{}
		}
		return decodeResponse[response](t, r)
	}
	a := create("Inherit", nil, 201)
	b := create("Explicit", 3, 201)
	if a.Max != 10 || b.Max != 3 {
		t.Fatalf("limits not inherited/explicit: %+v %+v", a, b)
	}
	api.do(t, http.MethodPatch, "/api/v2/me", token, "", map[string]any{"max_items_per_digest": 1}, 200)
	path := fmt.Sprintf("/api/v2/subscriptions/%d", a.ID)
	a = decodeResponse[response](t, api.do(t, http.MethodGet, path, token, "", nil, 200))
	if a.Max != 10 {
		t.Fatal("default change mutated existing subscription")
	}
	for _, bad := range []any{0, 51, -1, 1.5, "2", nil} {
		api.do(t, http.MethodPatch, path, token, `"1"`, map[string]any{"max_items_per_digest": bad}, 400)
	}
	a = decodeResponse[response](t, api.do(t, http.MethodPatch, path, token, `"1"`, map[string]any{"max_items_per_digest": 2}, 200))
	if a.Max != 2 || a.Version != 2 {
		t.Fatalf("update %+v", a)
	}
	b = decodeResponse[response](t, api.do(t, http.MethodGet, fmt.Sprintf("/api/v2/subscriptions/%d", b.ID), token, "", nil, 200))
	if b.Max != 3 {
		t.Fatal("other subscription limit changed")
	}
	for _, bad := range []any{0, 51, -1, 1.5, "2"} {
		create("Bad", bad, 400)
	}
}
