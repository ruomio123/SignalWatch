package integration_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"testing"
	"time"

	"signalwatch/internal/matcher"
	"signalwatch/internal/paper"
	"signalwatch/internal/source"
	"signalwatch/internal/subscription"
	"signalwatch/internal/user"
)

func TestM3MatchedPaperAPIAndUserIsolation(t *testing.T) {
	database, sqlDB, _ := openM1TestDatabase(t)
	t.Cleanup(func() {
		if err := sqlDB.Close(); err != nil {
			t.Errorf("close isolated M3 API test connection pool: %v", err)
		}
	})

	now := time.Now().UTC().Truncate(time.Microsecond)
	nonce := strconv.FormatInt(now.UnixNano(), 36)
	emailA := "m3-paper-a-" + nonce + "@example.test"
	emailB := "m3-paper-b-" + nonce + "@example.test"
	var paperIDs []uint64
	t.Cleanup(func() {
		var userIDs []uint64
		_ = database.Model(&user.User{}).Where("email IN ?", []string{emailA, emailB}).
			Pluck("id", &userIDs).Error
		if len(userIDs) > 0 {
			_ = database.Unscoped().Where("user_id IN ?", userIDs).
				Delete(&subscription.Subscription{}).Error
		}
		if len(paperIDs) > 0 {
			_ = database.Where("id IN ?", paperIDs).Delete(&paper.Paper{}).Error
		}
		if len(userIDs) > 0 {
			_ = database.Where("id IN ?", userIDs).Delete(&user.User{}).Error
		}
	})

	var logs bytes.Buffer
	api := newM1TestAPI(t, database, sqlDB.PingContext, &logs)
	register := func(email string) (userResponse, string) {
		registered := decodeResponse[userResponse](t, api.do(
			t, http.MethodPost, "/api/v1/auth/register", "", "",
			map[string]any{"email": email, "password": testPassword}, http.StatusCreated,
		))
		login := decodeResponse[loginResponse](t, api.do(
			t, http.MethodPost, "/api/v1/auth/login", "", "",
			map[string]any{"email": email, "password": testPassword}, http.StatusOK,
		))
		return registered, login.AccessToken
	}
	_, tokenA := register(emailA)
	_, tokenB := register(emailB)

	var arXiv source.Source
	if err := database.Where("source_key = ?", "arxiv").Take(&arXiv).Error; err != nil {
		t.Fatalf("load arxiv source: %v", err)
	}
	createSubscription := func(token, name, category string, keywords []string) subscriptionResponse {
		return decodeResponse[subscriptionResponse](t, api.do(
			t, http.MethodPost, "/api/v1/subscriptions", token, "",
			map[string]any{
				"source_id": arXiv.ID, "name": name, "enabled": true,
				"rules": map[string]any{"categories": []string{category}, "include_keywords": keywords},
			},
			http.StatusCreated,
		))
	}
	subscriptionA := createSubscription(tokenA, "A agents", "cs.AI", []string{"agent"})
	subscriptionB := createSubscription(tokenB, "B agents", "cs.AI", []string{"agent"})
	createSubscription(tokenB, "B vision", "cs.CV", []string{"vision"})

	observedAt := time.Now().UTC().Add(time.Minute).Truncate(time.Microsecond)
	inserted, err := paper.NewRepository(database).Upsert(t.Context(), arXiv.ID, []paper.Record{
		{
			ArXivID: "2609.shared-" + nonce, Title: "An Agent Research System", Abstract: "Shared result.",
			Authors: []string{"Ada"}, Categories: []string{"cs.AI"},
			PublishedAt: now, ArXivUpdatedAt: now,
			ArXivURL: "https://arxiv.org/abs/2609.shared-" + nonce,
			PDFURL:   "https://arxiv.org/pdf/2609.shared-" + nonce,
		},
		{
			ArXivID: "2609.vision-" + nonce, Title: "A Vision Model", Abstract: "Only user B follows vision.",
			Authors: []string{"Grace"}, Categories: []string{"cs.CV"},
			PublishedAt: now, ArXivUpdatedAt: now,
			ArXivURL: "https://arxiv.org/abs/2609.vision-" + nonce,
			PDFURL:   "https://arxiv.org/pdf/2609.vision-" + nonce,
		},
	}, observedAt)
	if err != nil || len(inserted.Papers) != 2 {
		t.Fatalf("insert API paper fixtures: result=%+v error=%v", inserted, err)
	}
	for _, stored := range inserted.Papers {
		paperIDs = append(paperIDs, stored.ID)
	}
	matchService, err := matcher.NewService(matcher.NewRepository(database), time.Now)
	if err != nil {
		t.Fatalf("create matcher: %v", err)
	}
	for _, stored := range inserted.Papers {
		if _, err := matchService.Match(t.Context(), stored.ID); err != nil {
			t.Fatalf("match paper %d: %v", stored.ID, err)
		}
	}

	pageA := decodeResponse[paper.QueryPage](t, api.do(
		t, http.MethodGet, "/api/v1/papers?page=1&page_size=20", tokenA, "", nil, http.StatusOK,
	))
	pageB := decodeResponse[paper.QueryPage](t, api.do(
		t, http.MethodGet, "/api/v1/papers?page=1&page_size=20", tokenB, "", nil, http.StatusOK,
	))
	if pageA.Total != 1 || len(pageA.Items) != 1 || len(pageA.Items[0].Matches) != 1 ||
		pageA.Items[0].Matches[0].SubscriptionID != subscriptionA.ID {
		t.Fatalf("user A paper page is not ownership-scoped: %+v", pageA)
	}
	if pageB.Total != 2 || len(pageB.Items) != 2 {
		t.Fatalf("user B should see two deduplicated papers: %+v", pageB)
	}

	filtered := decodeResponse[paper.QueryPage](t, api.do(
		t, http.MethodGet,
		fmt.Sprintf("/api/v1/papers?subscription_id=%d", subscriptionB.ID),
		tokenA, "", nil, http.StatusOK,
	))
	if filtered.Total != 0 || len(filtered.Items) != 0 {
		t.Fatalf("foreign subscription filter exposed papers: %+v", filtered)
	}

	sharedDetail := decodeResponse[paper.PublicPaper](t, api.do(
		t, http.MethodGet, fmt.Sprintf("/api/v1/papers/%d", inserted.Papers[0].ID),
		tokenA, "", nil, http.StatusOK,
	))
	if len(sharedDetail.Matches) != 1 || sharedDetail.Matches[0].SubscriptionID != subscriptionA.ID ||
		len(sharedDetail.Matches[0].MatchedKeywords) != 1 ||
		sharedDetail.Matches[0].MatchedKeywords[0] != "agent" {
		t.Fatalf("paper detail did not preserve owned match reason: %+v", sharedDetail)
	}
	assertAPIError(t, api.do(
		t, http.MethodGet, fmt.Sprintf("/api/v1/papers/%d", inserted.Papers[1].ID),
		tokenA, "", nil, http.StatusNotFound,
	), paper.CodePaperNotFound, "paper not found")
}

func TestM3MatcherRulesAndConcurrentIdempotency(t *testing.T) {
	database, sqlDB, _ := openM1TestDatabase(t)
	t.Cleanup(func() {
		if err := sqlDB.Close(); err != nil {
			t.Errorf("close isolated M3 test connection pool: %v", err)
		}
	})

	now := time.Now().UTC().Truncate(time.Microsecond)
	nonce := strconv.FormatInt(now.UnixNano(), 36)
	endpoint := "https://export.arxiv.org/api/query"
	sourceRecord := source.Source{
		SourceKey: "m3-arxiv-" + nonce, Kind: source.KindArXiv,
		Name: "M3 arXiv fixture", Endpoint: &endpoint, Enabled: true,
		ConfigJSON: json.RawMessage(
			`{"allowed_categories":["cs.AI","cs.LG","cs.CV"],"rule_types":["category","include_keyword"]}`,
		),
		CreatedAt: now, UpdatedAt: now,
	}
	if err := database.Create(&sourceRecord).Error; err != nil {
		t.Fatalf("create source: %v", err)
	}
	userRecord := user.User{
		Email: "m3-" + nonce + "@example.test", PasswordHash: "not-used-in-this-test",
		Timezone: "UTC", DigestTime: "08:00:00", MaxItemsPerDigest: 50,
		Status: "active", CreatedAt: now, UpdatedAt: now,
	}
	if err := database.Create(&userRecord).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}

	var subscriptionIDs []uint64
	var paperID uint64
	t.Cleanup(func() {
		if paperID != 0 {
			_ = database.Where("paper_id = ?", paperID).Delete(&paper.SubscriptionPaper{}).Error
			_ = database.Delete(&paper.Paper{}, paperID).Error
		}
		if len(subscriptionIDs) > 0 {
			_ = database.Unscoped().Where("id IN ?", subscriptionIDs).
				Delete(&subscription.Subscription{}).Error
		}
		_ = database.Delete(&sourceRecord).Error
		_ = database.Delete(&userRecord).Error
	})

	firstSeen := now.Add(10 * time.Minute)
	createSubscription := func(
		name, category, keywords string,
		enabled bool,
		createdAt time.Time,
		deleted bool,
	) uint64 {
		var deletedAt *time.Time
		if deleted {
			value := createdAt.Add(time.Minute)
			deletedAt = &value
		}
		record := subscription.Subscription{
			UserID: userRecord.ID, SourceID: sourceRecord.ID, Name: name,
			Category: category, KeywordsJSON: json.RawMessage(keywords),
			Enabled: enabled, Version: subscription.InitialVersion,
			CreatedAt: createdAt, UpdatedAt: createdAt, DeletedAt: deletedAt,
		}
		if err := database.Create(&record).Error; err != nil {
			t.Fatalf("create subscription %s: %v", name, err)
		}
		subscriptionIDs = append(subscriptionIDs, record.ID)
		return record.ID
	}

	multiKeywordID := createSubscription(
		"cross-category keyword OR", "cs.AI", `["tool   USE","RAG","absent"]`,
		true, firstSeen, false,
	)
	emptyKeywordID := createSubscription(
		"category only", "cs.LG", `[]`, true, firstSeen.Add(-time.Minute), false,
	)
	createSubscription(
		"authors must not match", "cs.AI", `["Famous Author"]`, true, firstSeen, false,
	)
	createSubscription(
		"wrong category", "cs.CV", `[]`, true, firstSeen, false,
	)
	createSubscription(
		"created after first seen", "cs.AI", `[]`, true, firstSeen.Add(time.Microsecond), false,
	)
	createSubscription(
		"disabled", "cs.AI", `[]`, false, firstSeen, false,
	)
	createSubscription(
		"soft deleted", "cs.AI", `[]`, true, firstSeen, true,
	)

	paperRepository := paper.NewRepository(database)
	upserted, err := paperRepository.Upsert(t.Context(), sourceRecord.ID, []paper.Record{{
		ArXivID: "2609." + nonce,
		Title:   "A TOOL use system", Abstract: "Retrieval with RAG in the abstract.",
		Authors: []string{"Famous Author"}, Categories: []string{"cs.LG", "cs.AI"},
		PublishedAt: firstSeen.Add(-time.Hour), ArXivUpdatedAt: firstSeen.Add(-time.Hour),
		ArXivURL: "https://arxiv.org/abs/2609." + nonce,
		PDFURL:   "https://arxiv.org/pdf/2609." + nonce,
	}}, firstSeen)
	if err != nil || len(upserted.Papers) != 1 {
		t.Fatalf("insert matcher paper: result=%+v error=%v", upserted, err)
	}
	paperID = upserted.Papers[0].ID

	firstMatchedAt := firstSeen.Add(time.Hour)
	service, err := matcher.NewService(matcher.NewRepository(database), func() time.Time {
		return firstMatchedAt
	})
	if err != nil {
		t.Fatalf("create matcher: %v", err)
	}
	runMatchesConcurrently(t, service, paperID, 8)

	var matches []paper.SubscriptionPaper
	if err := database.Where("paper_id = ?", paperID).
		Order("subscription_id ASC").Find(&matches).Error; err != nil {
		t.Fatalf("load matches: %v", err)
	}
	if len(matches) != 2 || matches[0].SubscriptionID != multiKeywordID ||
		matches[1].SubscriptionID != emptyKeywordID {
		t.Fatalf("only eligible category/keyword/from-now subscriptions should match: %+v", matches)
	}
	assertMatchedKeywords(t, matches[0].MatchedKeywordsJSON, []string{"tool USE", "RAG"})
	assertMatchedKeywords(t, matches[1].MatchedKeywordsJSON, []string{})
	for _, match := range matches {
		if !match.MatchedAt.Equal(firstMatchedAt) {
			t.Fatalf("unexpected initial matched_at: %+v", match)
		}
	}

	// A later rerun can still find the same relation through updated metadata,
	// but the unique key must preserve the original explanation and timestamp.
	if err := database.Model(&subscription.Subscription{}).
		Where("id = ?", multiKeywordID).
		Update("keywords_json", json.RawMessage(`["RAG"]`)).Error; err != nil {
		t.Fatalf("update subscription keywords: %v", err)
	}
	laterService, err := matcher.NewService(matcher.NewRepository(database), func() time.Time {
		return firstMatchedAt.Add(time.Hour)
	})
	if err != nil {
		t.Fatalf("create later matcher: %v", err)
	}
	runMatchesConcurrently(t, laterService, paperID, 8)

	var preserved paper.SubscriptionPaper
	if err := database.Where(
		"subscription_id = ? AND paper_id = ?", multiKeywordID, paperID,
	).Take(&preserved).Error; err != nil {
		t.Fatalf("load preserved match: %v", err)
	}
	if !preserved.MatchedAt.Equal(firstMatchedAt) {
		t.Fatalf("rerun overwrote first matched_at: %v", preserved.MatchedAt)
	}
	assertMatchedKeywords(t, preserved.MatchedKeywordsJSON, []string{"tool USE", "RAG"})

	var count int64
	if err := database.Model(&paper.SubscriptionPaper{}).
		Where("paper_id = ?", paperID).Count(&count).Error; err != nil {
		t.Fatalf("count idempotent matches: %v", err)
	}
	if count != 2 {
		t.Fatalf("concurrent reruns created duplicates: count=%d", count)
	}
}

func runMatchesConcurrently(t *testing.T, service *matcher.Service, paperID uint64, count int) {
	t.Helper()
	var workers sync.WaitGroup
	errorsChannel := make(chan error, count)
	workers.Add(count)
	for index := 0; index < count; index++ {
		go func() {
			defer workers.Done()
			if _, err := service.Match(t.Context(), paperID); err != nil {
				errorsChannel <- err
			}
		}()
	}
	workers.Wait()
	close(errorsChannel)
	for err := range errorsChannel {
		t.Errorf("concurrent match failed: %v", err)
	}
	if t.Failed() {
		t.FailNow()
	}
}

func assertMatchedKeywords(t *testing.T, raw json.RawMessage, expected []string) {
	t.Helper()
	var actual []string
	if err := json.Unmarshal(raw, &actual); err != nil {
		t.Fatalf("decode matched keywords: %v", err)
	}
	if fmt.Sprint(actual) != fmt.Sprint(expected) {
		t.Fatalf("matched keywords=%v want=%v", actual, expected)
	}
}
