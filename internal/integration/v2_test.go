package integration_test

import (
	"bytes"
	"net/http"
	"strconv"
	"testing"
	"time"

	"signalwatch/internal/paper"
	"signalwatch/internal/source"
	"signalwatch/internal/subscription"
	"signalwatch/internal/user"
)

func TestV2SubscriptionCreationAtomicallyBackfillsRecentLocalPapers(t *testing.T) {
	database, sqlDB, _ := openM1TestDatabase(t)
	t.Cleanup(func() {
		if err := sqlDB.Close(); err != nil {
			t.Errorf("close isolated V2 test connection pool: %v", err)
		}
	})
	now := time.Now().UTC().Truncate(time.Microsecond)
	nonce := strconv.FormatInt(now.UnixNano(), 36)
	email := "v2-backfill-" + nonce + "@example.test"
	var paperIDs []uint64
	t.Cleanup(func() {
		var userIDs []uint64
		_ = database.Model(&user.User{}).Where("email = ?", email).Pluck("id", &userIDs).Error
		if len(userIDs) > 0 {
			_ = database.Unscoped().Where("user_id IN ?", userIDs).Delete(&subscription.Subscription{}).Error
			_ = database.Where("id IN ?", userIDs).Delete(&user.User{}).Error
		}
		if len(paperIDs) > 0 {
			_ = database.Where("id IN ?", paperIDs).Delete(&paper.Paper{}).Error
		}
	})

	var arXiv source.Source
	if err := database.Where("source_key = ?", "arxiv").Take(&arXiv).Error; err != nil {
		t.Fatalf("load arxiv source: %v", err)
	}
	inserted, err := paper.NewRepository(database).Upsert(t.Context(), arXiv.ID, []paper.Record{
		{
			ArXivID: "v2-recent-" + nonce, Title: "Reliable Agent Planning", Abstract: "A tool-using system.",
			Authors: []string{"Ada"}, Categories: []string{"cs.LG", "cs.AI"},
			PublishedAt: now.Add(-24 * time.Hour), ArXivUpdatedAt: now,
			ArXivURL: "https://arxiv.org/abs/v2-recent-" + nonce,
			PDFURL:   "https://arxiv.org/pdf/v2-recent-" + nonce,
		},
		{
			ArXivID: "v2-keyword-miss-" + nonce, Title: "Unrelated Work", Abstract: "No matching phrase.",
			Authors: []string{"Grace"}, Categories: []string{"cs.AI"},
			PublishedAt: now.Add(-24 * time.Hour), ArXivUpdatedAt: now,
			ArXivURL: "https://arxiv.org/abs/v2-keyword-miss-" + nonce,
			PDFURL:   "https://arxiv.org/pdf/v2-keyword-miss-" + nonce,
		},
		{
			ArXivID: "v2-old-" + nonce, Title: "Old Agent Planning", Abstract: "Outside the window.",
			Authors: []string{"Lin"}, Categories: []string{"cs.AI"},
			PublishedAt: now.Add(-8 * 24 * time.Hour), ArXivUpdatedAt: now.Add(-8 * 24 * time.Hour),
			ArXivURL: "https://arxiv.org/abs/v2-old-" + nonce,
			PDFURL:   "https://arxiv.org/pdf/v2-old-" + nonce,
		},
	}, now)
	if err != nil || len(inserted.Papers) != 3 {
		t.Fatalf("insert backfill fixtures: result=%+v error=%v", inserted, err)
	}
	for _, stored := range inserted.Papers {
		paperIDs = append(paperIDs, stored.ID)
	}

	var logs bytes.Buffer
	api := newM1TestAPI(t, database, sqlDB.PingContext, &logs)
	decodeResponse[userResponse](t, api.do(
		t, http.MethodPost, "/api/v1/auth/register", "", "",
		map[string]any{"email": email, "password": testPassword}, http.StatusCreated,
	))
	login := decodeResponse[loginResponse](t, api.do(
		t, http.MethodPost, "/api/v1/auth/login", "", "",
		map[string]any{"email": email, "password": testPassword}, http.StatusOK,
	))
	created := decodeResponse[subscriptionResponse](t, api.do(
		t, http.MethodPost, "/api/v1/subscriptions", login.AccessToken, "",
		map[string]any{
			"source_id": arXiv.ID, "name": "Recent agents", "enabled": true,
			"rules": map[string]any{"categories": []string{"cs.AI"}, "include_keywords": []string{"agent"}},
		}, http.StatusCreated,
	))

	var matches []paper.SubscriptionPaper
	if err := database.Where("subscription_id = ?", created.ID).Order("paper_id ASC").Find(&matches).Error; err != nil {
		t.Fatalf("load atomic backfill matches: %v", err)
	}
	if len(matches) != 1 || matches[0].PaperID != inserted.Papers[0].ID ||
		string(matches[0].MatchedKeywordsJSON) != `["agent"]` || matches[0].DeliveredAt != nil {
		t.Fatalf("unexpected seven-day backfill results: %+v", matches)
	}

	disabled := false
	paused := decodeResponse[subscriptionResponse](t, api.do(
		t, http.MethodPost, "/api/v1/subscriptions", login.AccessToken, "",
		map[string]any{
			"source_id": arXiv.ID, "name": "Paused agents", "enabled": disabled,
			"rules": map[string]any{"categories": []string{"cs.AI"}, "include_keywords": []string{"agent"}},
		}, http.StatusCreated,
	))
	var pausedCount int64
	if err := database.Model(&paper.SubscriptionPaper{}).Where("subscription_id = ?", paused.ID).Count(&pausedCount).Error; err != nil {
		t.Fatalf("count paused backfill matches: %v", err)
	}
	if pausedCount != 0 {
		t.Fatalf("disabled subscription must not backfill, got %d matches", pausedCount)
	}
}
