package ai

import (
	"context"
	"crypto/rand"
	"errors"
	"signalwatch/internal/generation"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func fixtureCall(f *aiIntegrationFixture, feature string) CallRecord {
	return CallRecord{ID: rand.Text(), UserID: f.users[0].ID, Feature: feature, Provider: "glm", Model: "glm-4.7-flash", Status: "reserved", CreatedAt: f.now, LeaseUntil: f.now.Add(time.Minute)}
}
func TestMySQLCallAdmissionAndDailyPolicy(t *testing.T) {
	f := newAIIntegrationFixture(t)
	ctx := t.Context()
	s := NewMySQLCallStore(f.db)
	c := fixtureCall(f, FeaturePaper)
	mustAI(t, s.Admit(ctx, c, DefaultCallPolicy()))
	var accepted atomic.Int32
	var wg sync.WaitGroup
	for range 12 {
		wg.Go(func() {
			other := NewMySQLCallStore(f.db)
			e := other.Admit(ctx, fixtureCall(f, FeatureDigest), DefaultCallPolicy())
			var ce *CallError
			if e == nil {
				accepted.Add(1)
			} else if !errors.As(e, &ce) || ce.Code != "call_in_progress" {
				t.Errorf("concurrent admission=%v", e)
			}
		})
	}
	wg.Wait()
	if accepted.Load() != 0 {
		t.Fatal("overlapping call admitted")
	}
	mustAI(t, s.Start(ctx, c.ID, f.now))
	result := generation.Result{InputTokens: 3, OutputTokens: 2, UsageKnown: true}
	mustAI(t, s.Finish(ctx, c.ID, result, nil, f.now))
	mustAI(t, s.Finish(ctx, c.ID, result, nil, f.now))
	mustAI(t, s.Release(ctx, c, f.now))
	e := s.Admit(ctx, fixtureCall(f, FeatureDigest), DefaultCallPolicy())
	var ce *CallError
	if !errors.As(e, &ce) || ce.Code != "rate_limited" {
		t.Fatalf("shared generation rate=%v", e)
	}
	rows, e := s.Usage(ctx, c.UserID, f.now, f.now)
	mustAI(t, e)
	if len(rows) != 1 || rows[0].Calls != 1 || rows[0].Succeeded != 1 || rows[0].InputTokens != 3 {
		t.Fatalf("settlement duplicated: %+v", rows)
	}
	f.now = f.now.Add(10 * time.Second)
	e = s.Admit(ctx, fixtureCall(f, FeaturePaper), CallPolicy{PaperDailyLimit: 1})
	if !errors.As(e, &ce) || ce.Code != "daily_limit" || ce.RetryAt == nil {
		t.Fatalf("daily cap=%v", e)
	}
	f.now = f.now.Add(24 * time.Hour)
	c = fixtureCall(f, FeaturePaper)
	mustAI(t, s.Admit(ctx, c, CallPolicy{PaperDailyLimit: 1}))
	mustAI(t, s.Release(ctx, c, f.now))
}
func TestMySQLCallUnlimitedAndFailures(t *testing.T) {
	f := newAIIntegrationFixture(t)
	ctx := t.Context()
	r := f.configurations.calls
	for range 7 {
		mustAI(t, f.configurations.test(ctx, f.users[0].ID, "glm", "glm-4.7-flash", "valid-test-key"))
	}
	r.factory = func([]string, string, string, string) (Generator, error) {
		return GeneratorFunc(func(context.Context, string, []byte, int) (generation.Result, error) {
			return generation.Result{}, &generation.Failure{Code: "timeout"}
		}), nil
	}
	for range 22 {
		_, e := r.Run(ctx, CallRequest{UserID: f.users[0].ID, Feature: FeaturePaper, Provider: "glm", Model: "glm-4.7-flash", Key: "valid-test-key", MaxTokens: 100})
		var ce *CallError
		if !errors.As(e, &ce) || ce.Code != "timeout" || ce.CallID == "" {
			t.Fatalf("failure=%v", e)
		}
	}
	rows, e := r.store.Usage(ctx, f.users[0].ID, f.now, f.now)
	mustAI(t, e)
	for _, row := range rows {
		if row.Feature == FeaturePaper && (row.Calls != 22 || row.Unknown != 22) {
			t.Fatalf("unknown accounting=%+v", row)
		}
		if row.Feature == FeatureConfigTest && row.Calls != 7 {
			t.Fatalf("test calls=%+v", row)
		}
	}
}
func TestMySQLCallRecoveryCancellationAndRetention(t *testing.T) {
	f := newAIIntegrationFixture(t)
	ctx := t.Context()
	s := NewMySQLCallStore(f.db)
	cancelled := fixtureCall(f, FeaturePaper)
	mustAI(t, s.Admit(ctx, cancelled, CallPolicy{}))
	mustAI(t, s.Release(ctx, cancelled, f.now))
	rows, e := s.Usage(ctx, cancelled.UserID, f.now, f.now)
	mustAI(t, e)
	if len(rows) != 0 {
		t.Fatal("cancelled reservation counted")
	}
	started := fixtureCall(f, FeaturePaper)
	mustAI(t, s.Admit(ctx, started, CallPolicy{}))
	mustAI(t, s.Start(ctx, started.ID, f.now))
	old := f.now
	f.now = f.now.Add(61 * time.Second)
	mustAI(t, s.Recover(ctx, f.now))
	mustAI(t, s.Recover(ctx, f.now))
	mustAI(t, s.Finish(ctx, started.ID, generation.Result{InputTokens: 999}, nil, f.now))
	rows, e = s.Usage(ctx, started.UserID, old, f.now)
	mustAI(t, e)
	if len(rows) != 1 || rows[0].Unknown != 1 || rows[0].Calls != 1 || rows[0].InputTokens != 0 {
		t.Fatalf("recovery=%+v", rows)
	}
	mustAI(t, s.Recover(ctx, f.now.AddDate(0, 0, 31)))
	page, e := s.List(ctx, started.UserID, "", "", 1, 10)
	mustAI(t, e)
	if page.Total != 0 {
		t.Fatal("old details retained")
	}
	rows, e = s.Usage(ctx, started.UserID, old, old)
	mustAI(t, e)
	if len(rows) != 1 {
		t.Fatal("history deleted")
	}
}
func TestMySQLCallBeforeStartAndValidation(t *testing.T) {
	f := newAIIntegrationFixture(t)
	r := f.configurations.calls
	ctx := t.Context()
	before := f.calls.Load()
	req := CallRequest{UserID: f.users[0].ID, Feature: FeatureConfigTest, Provider: "glm", Model: "glm-4.7-flash", Key: "valid-test-key", MaxTokens: 1024, BeforeStart: func(context.Context) error { return ErrLeaseLost }}
	_, err := r.Run(ctx, req)
	if !errors.Is(err, ErrLeaseLost) || f.calls.Load() != before {
		t.Fatal("preflight failure called vendor")
	}
	req.BeforeStart = nil
	req.Validate = func(generation.Result) error { return &generation.Failure{Code: "invalid_output"} }
	_, err = r.Run(ctx, req)
	var ce *CallError
	if !errors.As(err, &ce) || ce.Code != "invalid_output" || f.calls.Load() != before+1 {
		t.Fatalf("validation/retry=%v", err)
	}
	rows, err := r.store.Usage(ctx, req.UserID, f.now, f.now)
	mustAI(t, err)
	if rows[0].Calls != 1 || rows[0].Failed != 1 || rows[0].InputTokens != 2 {
		t.Fatalf("known usage lost: %+v", rows)
	}
}
func TestMySQLConfigurationReuseAndConflictDuringValidation(t *testing.T) {
	f := newAIIntegrationFixture(t)
	ctx := t.Context()
	u := f.users[0].ID
	v := uint64(1)
	c, e := f.configurations.Put(ctx, u, "glm", "glm-5.2", "", &v)
	mustAI(t, e)
	if c.ModelID != "glm-5.2" || c.Version != 2 {
		t.Fatal("model switch failed")
	}
	v = c.Version
	before := f.calls.Load()
	_, e = f.configurations.Put(ctx, u, "qwen", "qwen3.8-flash", "", &v)
	if !errors.Is(e, ErrInvalidSecret) || f.calls.Load() != before {
		t.Fatal("provider switch reused key")
	}
	factory := f.configurations.calls.factory
	f.configurations.calls.factory = func(a []string, p, m, k string) (Generator, error) {
		g, e := factory(a, p, m, k)
		return GeneratorFunc(func(c context.Context, p string, b []byte, n int) (generation.Result, error) {
			mustAI(t, f.configurations.Delete(ctx, u, v))
			return g.GenerateLimit(c, p, b, n)
		}), e
	}
	_, e = f.configurations.Put(ctx, u, "glm", "glm-4.7-flash", "", &v)
	if !errors.Is(e, ErrConfigurationConflict) {
		t.Fatalf("deleted config overwritten: %v", e)
	}
}

func TestMySQLExpiredCallCannotSaveConfiguration(t *testing.T) {
	f := newAIIntegrationFixture(t)
	ctx := t.Context()
	old := f.now
	factory := f.configurations.calls.factory
	f.configurations.calls.factory = func(a []string, p, m, k string) (Generator, error) {
		g, e := factory(a, p, m, k)
		return GeneratorFunc(func(c context.Context, p string, b []byte, n int) (generation.Result, error) {
			f.now = f.now.Add(61 * time.Second)
			return g.GenerateLimit(c, p, b, n)
		}), e
	}
	v := uint64(1)
	_, e := f.configurations.Put(ctx, f.users[0].ID, "glm", "glm-5.2", "", &v)
	var ce *CallError
	if !errors.As(e, &ce) || ce.Code != "result_unknown" {
		t.Fatalf("expired call=%v", e)
	}
	c, e := f.configurations.Get(ctx, f.users[0].ID)
	mustAI(t, e)
	if c.Version != v || c.ModelID != "glm-4.7-flash" {
		t.Fatal("expired call committed")
	}
	rows, e := f.configurations.calls.store.Usage(ctx, f.users[0].ID, old, f.now)
	mustAI(t, e)
	if len(rows) != 1 || rows[0].Unknown != 1 {
		t.Fatalf("expired call usage=%+v", rows)
	}
}
func TestMySQLZeroAttemptFailureCanBeRetriedOnce(t *testing.T) {
	f := newAIIntegrationFixture(t)
	ctx := t.Context()
	_, e := f.service.Summary(ctx, f.users[0].ID, f.papers[0].ID, "zh", true)
	mustAI(t, e)
	var task Task
	mustAI(t, f.db.Table("paper_ai_summaries").Where("owner_user_id=?", f.users[0].ID).Take(&task).Error)
	task.Kind = PaperKind
	mustAI(t, f.db.Table("paper_ai_summaries").Where("id=?", task.ID).Updates(map[string]any{"status": "failed", "attempts": 0, "failure_code": "budget_exhausted"}).Error)
	mustAI(t, f.service.repo.Retry(ctx, task, f.now))
	if !errors.Is(f.service.repo.Retry(ctx, task, f.now), ErrLeaseLost) {
		t.Fatal("second retry reported a false transition")
	}
}
func TestMySQLCallAccountingAcrossUTCMidnight(t *testing.T) {
	f := newAIIntegrationFixture(t)
	ctx := t.Context()
	f.now = time.Date(2036, 9, 9, 23, 59, 59, 0, time.UTC)
	s := NewMySQLCallStore(f.db)
	c := fixtureCall(f, FeaturePaper)
	mustAI(t, s.Admit(ctx, c, CallPolicy{PaperDailyLimit: 1}))
	f.now = f.now.Add(2 * time.Second)
	mustAI(t, s.Start(ctx, c.ID, f.now))
	mustAI(t, s.Finish(ctx, c.ID, generation.Result{}, nil, f.now))
	mustAI(t, s.Release(ctx, c, f.now))
	rows, e := s.Usage(ctx, c.UserID, c.CreatedAt, f.now)
	mustAI(t, e)
	if len(rows) != 1 || rows[0].Day != "2036-09-09" || rows[0].Calls != 1 || rows[0].Succeeded != 1 {
		t.Fatalf("UTC accounting=%+v", rows)
	}
	next := fixtureCall(f, FeaturePaper)
	mustAI(t, s.Admit(ctx, next, CallPolicy{PaperDailyLimit: 1}))
	mustAI(t, s.Release(ctx, next, f.now))
}

func TestMySQLRejectedSavedCredentialMarksOnlyCurrentRevision(t *testing.T) {
	f := newAIIntegrationFixture(t)
	ctx := t.Context()
	f.configurations.calls.factory = func([]string, string, string, string) (Generator, error) {
		return GeneratorFunc(func(context.Context, string, []byte, int) (generation.Result, error) {
			return generation.Result{}, &generation.Failure{Code: "credential_rejected"}
		}), nil
	}
	v := uint64(1)
	_, e := f.configurations.Put(ctx, f.users[0].ID, "glm", "glm-5.2", "replacement-secret", &v)
	if e == nil {
		t.Fatal("rejected replacement succeeded")
	}
	c, e := f.configurations.Get(ctx, f.users[0].ID)
	mustAI(t, e)
	if !c.Usable {
		t.Fatal("rejected replacement invalidated original")
	}
	_, e = f.configurations.TestSaved(ctx, f.users[0].ID)
	if e == nil {
		t.Fatal("rejected test succeeded")
	}
	c, e = f.configurations.Get(ctx, f.users[0].ID)
	mustAI(t, e)
	if c.Usable || c.Status != ConfigurationInvalid {
		t.Fatal("rejected saved key remained usable")
	}
}
