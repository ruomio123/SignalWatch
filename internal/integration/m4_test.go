package integration_test

import (
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
		Status: user.StatusActive, Role: user.RoleUser, CreatedAt: now, UpdatedAt: now,
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
			MaxItemsPerDigest: 2, UserID: userRecord.ID, SourceID: sourceRecord.ID, Name: name,
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
	tertiary := createSubscription("Tertiary", true)
	disabled := createSubscription("Disabled", false)
	if err := database.Model(&secondary).Update("max_items_per_digest", 1).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.Model(&tertiary).Update("max_items_per_digest", 3).Error; err != nil {
		t.Fatal(err)
	}

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
	for _, id := range []uint64{paperIDs[0], paperIDs[2], paperIDs[3]} {
		relations = append(relations, paper.SubscriptionPaper{SubscriptionID: tertiary.ID, PaperID: id, MatchedKeywordsJSON: json.RawMessage(`[]`), MatchedAt: matchedAt})
	}
	if err := database.Create(&relations).Error; err != nil {
		t.Fatalf("create M4 matches: %v", err)
	}

	repository := digest.NewRepository(database)
	candidates, err := repository.ListCandidates(t.Context(), userRecord.ID, primary.ID, 2)
	if err != nil {
		t.Fatalf("list M4 candidates: %v", err)
	}
	if len(candidates) != 2 || candidates[0].PaperID != paperIDs[0] ||
		candidates[1].PaperID != paperIDs[1] || len(candidates[0].Matches) != 1 {
		t.Fatalf("candidate dedup/exclusion/limit failed: %+v", candidates)
	}

	for _, tc := range []struct {
		userID, subID uint64
		want          int64
	}{
		{userRecord.ID, primary.ID, 2}, {userRecord.ID, secondary.ID, 0},
		{userRecord.ID, disabled.ID, 0}, {userRecord.ID + 1000000, primary.ID, 0},
	} {
		n, err := repository.CountRemaining(t.Context(), tc.userID, tc.subID, paperIDs[:2])
		if err != nil || n != tc.want {
			t.Fatalf("remaining isolation: got %d want %d: %v", n, tc.want, err)
		}
	}
	job := digest.Job{SubscriptionID: primary.ID, UserID: userRecord.ID, LocalDate: now.Format("2006-01-02")}
	coordinator := digest.NewMySQLDeliveryStore(database)
	isComplete := func() bool {
		done, err := coordinator.IsComplete(t.Context(), job)
		if err != nil {
			t.Fatal(err)
		}
		return done
	}
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
	if isComplete() {
		t.Fatal("SMTP failure wrote the daily completion marker")
	}
	assertM4Undelivered(t, database, paperIDs[0], 4)

	smtpAddr := os.Getenv(m4TestSMTPAddrEnv)
	if smtpAddr == "" {
		t.Fatal("M4_TEST_SMTP_ADDR must point to isolated Mailpit")
	}
	mailpitSender, err := digest.NewSMTPSender(digest.SMTPConfig{
		Addr: smtpAddr, From: "SignalWatch <digest@signalwatch.local>", Timeout: 5 * time.Second,
	}, time.Now)
	if err != nil {
		t.Fatalf("create Mailpit sender: %v", err)
	}
	if err := database.Exec("UPDATE digest_deliveries SET next_retry_at=UTC_TIMESTAMP(6) WHERE subscription_id=?", job.SubscriptionID).Error; err != nil {
		t.Fatal(err)
	}
	processor, _ := digest.NewProcessor(repository, coordinator, mailpitSender, func() time.Time { return now })

	result, err := processor.Process(t.Context(), job)
	if err != nil {
		t.Fatalf("deliver M4 digest through Mailpit at %s: %v", smtpAddr, err)
	}
	if result.Items != 2 || result.MarkedRelations != 2 || !isComplete() {
		t.Fatalf("unexpected successful M4 result=%+v coordinator=%+v", result, coordinator)
	}
	assertM4Undelivered(t, database, paperIDs[0], 3)
	assertM4Undelivered(t, database, paperIDs[2], 2)
	assertM4Undelivered(t, database, paperIDs[3], 2)

	second, err := processor.Process(t.Context(), job)
	if err != nil || !second.AlreadyComplete {
		t.Fatalf("same-day rerun was not idempotent: result=%+v err=%v",
			second, err)
	}
	// The first subscription's completion and delivery cannot consume the others.
	for _, tc := range []struct {
		sub  subscription.Subscription
		want int
	}{{secondary, 1}, {tertiary, 3}} {
		c := digest.NewMySQLDeliveryStore(database)
		p, _ := digest.NewProcessor(repository, c, mailpitSender, func() time.Time { return now })
		j := digest.Job{UserID: userRecord.ID, SubscriptionID: tc.sub.ID, LocalDate: job.LocalDate}
		r, err := p.Process(t.Context(), j)
		if err != nil || r.Items != tc.want || r.MarkedRelations != int64(tc.want) {
			t.Fatalf("independent subscription result=%+v err=%v", r, err)
		}
	}
	assertM4Undelivered(t, database, paperIDs[0], 1) // paused subscription remains untouched
	// A queued subscription paused/deleted since scheduling must not send.
	job.SubscriptionID = disabled.ID
	c := digest.NewMySQLDeliveryStore(database)
	p, _ := digest.NewProcessor(repository, c, mailpitSender, func() time.Time { return now })
	if result, err := p.Process(t.Context(), job); err != nil || !result.Stale {
		t.Fatalf("paused subscription result=%+v error=%v", result, err)
	}
	job.SubscriptionID = primary.ID
	job.UserID++
	if _, err := p.Process(t.Context(), job); err == nil {
		t.Fatal("cross-user subscription sent")
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
