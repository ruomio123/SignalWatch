package integration_test

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"strconv"
	"testing"
	"time"

	"gorm.io/gorm"

	"signalwatch/internal/digest"
	"signalwatch/internal/paper"
	"signalwatch/internal/source"
	"signalwatch/internal/subscription"
	"signalwatch/internal/user"
)

const m4TestSMTPAddrEnv = "M4_TEST_SMTP_ADDR"

type integrationCoordinator struct {
	complete bool
	marked   int
}

func (coordinator *integrationCoordinator) Acquire(
	context.Context, digest.Job,
) (digest.ReleaseFunc, bool, error) {
	return func(context.Context) error { return nil }, true, nil
}
func (coordinator *integrationCoordinator) IsComplete(context.Context, digest.Job) (bool, error) {
	return coordinator.complete, nil
}
func (coordinator *integrationCoordinator) MarkComplete(context.Context, digest.Job) error {
	coordinator.complete = true
	coordinator.marked++
	return nil
}

func TestM4MailpitDigestDeliveryAndRetryBoundaries(t *testing.T) {
	database, sqlDB, _ := openM1TestDatabase(t)
	t.Cleanup(func() {
		if err := sqlDB.Close(); err != nil {
			t.Errorf("close isolated M4 test connection pool: %v", err)
		}
	})

	now := time.Now().UTC().Truncate(time.Microsecond)
	nonce := strconv.FormatInt(now.UnixNano(), 36)
	endpoint := "https://export.arxiv.org/api/query"
	sourceRecord := source.Source{
		SourceKey: "m4-arxiv-" + nonce, Kind: source.KindArXiv,
		Name: "M4 arXiv fixture", Endpoint: &endpoint, Enabled: true,
		ConfigJSON: json.RawMessage(
			`{"allowed_categories":["cs.AI"],"rule_types":["category","include_keyword"]}`,
		),
		CreatedAt: now, UpdatedAt: now,
	}
	if err := database.Create(&sourceRecord).Error; err != nil {
		t.Fatalf("create M4 source: %v", err)
	}
	userRecord := user.User{
		Email: "m4-" + nonce + "@example.test", PasswordHash: "not-used-in-this-test",
		Timezone: "UTC", DigestTime: "00:00:00", MaxItemsPerDigest: 2,
		Status: user.StatusActive, CreatedAt: now, UpdatedAt: now,
	}
	if err := database.Create(&userRecord).Error; err != nil {
		t.Fatalf("create M4 user: %v", err)
	}
	var subscriptionIDs, paperIDs []uint64
	t.Cleanup(func() {
		if len(subscriptionIDs) > 0 {
			_ = database.Unscoped().Where("id IN ?", subscriptionIDs).Delete(&subscription.Subscription{}).Error
		}
		if len(paperIDs) > 0 {
			_ = database.Where("id IN ?", paperIDs).Delete(&paper.Paper{}).Error
		}
		_ = database.Delete(&sourceRecord).Error
		_ = database.Delete(&userRecord).Error
	})

	createSubscription := func(name string, enabled bool) subscription.Subscription {
		record := subscription.Subscription{
			UserID: userRecord.ID, SourceID: sourceRecord.ID, Name: name,
			Category: "cs.AI", KeywordsJSON: json.RawMessage(`[]`), Enabled: enabled,
			Version: subscription.InitialVersion, CreatedAt: now, UpdatedAt: now,
		}
		if err := database.Create(&record).Error; err != nil {
			t.Fatalf("create M4 subscription: %v", err)
		}
		subscriptionIDs = append(subscriptionIDs, record.ID)
		return record
	}
	primary := createSubscription("Primary", true)
	secondary := createSubscription("Secondary", true)
	disabled := createSubscription("Disabled", false)

	records := make([]paper.Record, 4)
	for index := range records {
		id := "m4-" + nonce + "-" + strconv.Itoa(index+1)
		records[index] = paper.Record{
			ArXivID: id, Title: "M4 Paper " + strconv.Itoa(index+1), Abstract: "Digest integration fixture.",
			Authors: []string{"Ada"}, Categories: []string{"cs.AI"},
			PublishedAt: now.Add(time.Duration(index) * time.Minute), ArXivUpdatedAt: now,
			ArXivURL: "https://arxiv.org/abs/" + id, PDFURL: "https://arxiv.org/pdf/" + id,
		}
	}
	upserted, err := paper.NewRepository(database).Upsert(t.Context(), sourceRecord.ID, records, now)
	if err != nil || len(upserted.Papers) != 4 {
		t.Fatalf("insert M4 papers: result=%+v error=%v", upserted, err)
	}
	for _, stored := range upserted.Papers {
		paperIDs = append(paperIDs, stored.ID)
	}
	matchedAt := now
	deliveredAt := now.Add(time.Minute)
	relations := []paper.SubscriptionPaper{
		{SubscriptionID: primary.ID, PaperID: paperIDs[0], MatchedKeywordsJSON: json.RawMessage(`[]`), MatchedAt: matchedAt},
		{SubscriptionID: secondary.ID, PaperID: paperIDs[0], MatchedKeywordsJSON: json.RawMessage(`["agent"]`), MatchedAt: matchedAt},
		{SubscriptionID: disabled.ID, PaperID: paperIDs[0], MatchedKeywordsJSON: json.RawMessage(`[]`), MatchedAt: matchedAt},
		{SubscriptionID: primary.ID, PaperID: paperIDs[1], MatchedKeywordsJSON: json.RawMessage(`[]`), MatchedAt: matchedAt},
		{SubscriptionID: secondary.ID, PaperID: paperIDs[1], MatchedKeywordsJSON: json.RawMessage(`[]`), MatchedAt: matchedAt, DeliveredAt: &deliveredAt},
		{SubscriptionID: primary.ID, PaperID: paperIDs[2], MatchedKeywordsJSON: json.RawMessage(`[]`), MatchedAt: matchedAt},
		{SubscriptionID: primary.ID, PaperID: paperIDs[3], MatchedKeywordsJSON: json.RawMessage(`[]`), MatchedAt: matchedAt},
	}
	if err := database.Create(&relations).Error; err != nil {
		t.Fatalf("create M4 matches: %v", err)
	}

	repository := digest.NewRepository(database)
	candidates, err := repository.ListCandidates(t.Context(), userRecord.ID, 2)
	if err != nil {
		t.Fatalf("list M4 candidates: %v", err)
	}
	if len(candidates) != 2 || candidates[0].PaperID != paperIDs[0] ||
		candidates[1].PaperID != paperIDs[2] || len(candidates[0].Matches) != 2 {
		t.Fatalf("candidate dedup/exclusion/limit failed: %+v", candidates)
	}

	job := digest.Job{UserID: userRecord.ID, LocalDate: now.Format("2006-01-02")}
	coordinator := &integrationCoordinator{}
	closedListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve failing SMTP port: %v", err)
	}
	failingAddr := closedListener.Addr().String()
	_ = closedListener.Close()
	failingSender, _ := digest.NewSMTPSender(digest.SMTPConfig{
		Addr: failingAddr, From: "SignalWatch <digest@signalwatch.local>", Timeout: 250 * time.Millisecond,
	}, time.Now)
	failingProcessor, _ := digest.NewProcessor(repository, coordinator, failingSender, func() time.Time { return now })
	if _, err := failingProcessor.Process(t.Context(), job); err == nil {
		t.Fatal("expected SMTP failure")
	}
	if coordinator.complete || coordinator.marked != 0 {
		t.Fatal("SMTP failure wrote the daily completion marker")
	}
	assertM4Undelivered(t, database, paperIDs[0], 3)

	smtpAddr := os.Getenv(m4TestSMTPAddrEnv)
	if smtpAddr == "" {
		smtpAddr = "127.0.0.1:1025"
	}
	mailpitSender, err := digest.NewSMTPSender(digest.SMTPConfig{
		Addr: smtpAddr, From: "SignalWatch <digest@signalwatch.local>", Timeout: 5 * time.Second,
	}, time.Now)
	if err != nil {
		t.Fatalf("create Mailpit sender: %v", err)
	}
	processor, _ := digest.NewProcessor(repository, coordinator, mailpitSender, func() time.Time { return now })
	result, err := processor.Process(t.Context(), job)
	if err != nil {
		t.Fatalf("deliver M4 digest through Mailpit at %s: %v", smtpAddr, err)
	}
	if result.Items != 2 || result.MarkedRelations != 4 || !coordinator.complete || coordinator.marked != 1 {
		t.Fatalf("unexpected successful M4 result=%+v coordinator=%+v", result, coordinator)
	}
	assertM4Undelivered(t, database, paperIDs[0], 0)
	assertM4Undelivered(t, database, paperIDs[2], 0)
	assertM4Undelivered(t, database, paperIDs[3], 1)

	second, err := processor.Process(t.Context(), job)
	if err != nil || !second.AlreadyComplete || coordinator.marked != 1 {
		t.Fatalf("same-day rerun was not idempotent: result=%+v marker=%d err=%v",
			second, coordinator.marked, err)
	}
}

func assertM4Undelivered(t *testing.T, database *gorm.DB, paperID uint64, wanted int64) {
	t.Helper()
	var count int64
	if err := database.Model(&paper.SubscriptionPaper{}).
		Where("paper_id = ? AND delivered_at IS NULL", paperID).Count(&count).Error; err != nil {
		t.Fatalf("count M4 delivery state: %v", err)
	}
	if count != wanted {
		t.Fatalf("paper %d undelivered count=%d want=%d", paperID, count, wanted)
	}
}
