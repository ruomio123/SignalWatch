package ai

import (
	"errors"
	"signalwatch/internal/generation"
	"testing"
	"time"
)

func TestMySQLFirstCallAdmissionWithSubMicrosecondClock(t *testing.T) {
	for _, nanos := range []int{0, 499, 500, 999, 999999999} {
		t.Run(time.Duration(nanos).String(), func(t *testing.T) {
			f := newAIIntegrationFixture(t)
			f.now = time.Date(2036, 9, 9, 8, 0, 0, nanos, time.UTC)
			store := NewMySQLCallStore(f.db)
			c := fixtureCall(f, FeatureConfigTest)
			if err := store.Admit(t.Context(), c, DefaultCallPolicy()); err != nil {
				t.Fatalf("first call incorrectly blocked at %s: %v", f.now.Format(time.RFC3339Nano), err)
			}
			mustAI(t, store.Release(t.Context(), c, f.now))
		})
	}
}

func TestMySQLCooldownUsesStoragePrecisionAndDoesNotSlide(t *testing.T) {
	f := newAIIntegrationFixture(t)
	f.now = time.Date(2036, 9, 9, 8, 0, 0, 999, time.UTC)
	store := NewMySQLCallStore(f.db)
	first := fixtureCall(f, FeatureConfigTest)
	mustAI(t, store.Admit(t.Context(), first, DefaultCallPolicy()))
	mustAI(t, store.Start(t.Context(), first.ID, f.now))
	mustAI(t, store.Finish(t.Context(), first.ID, generation.Result{}, nil, f.now))
	mustAI(t, store.Release(t.Context(), first, f.now))
	deadline := callStorageTime(first.CreatedAt).Add(10 * time.Second)
	for _, before := range []time.Duration{time.Second, time.Nanosecond} {
		f.now = deadline.Add(-before)
		err := store.Admit(t.Context(), fixtureCall(f, FeatureConfigTest), DefaultCallPolicy())
		var blocked *CallError
		if !errors.As(err, &blocked) || blocked.Code != "rate_limited" || blocked.RetryAt == nil || !blocked.RetryAt.Equal(deadline) {
			t.Fatalf("cooldown changed or ended early: %+v", err)
		}
	}
	f.now = deadline
	next := fixtureCall(f, FeatureConfigTest)
	mustAI(t, store.Admit(t.Context(), next, DefaultCallPolicy()))
	mustAI(t, store.Release(t.Context(), next, f.now))
}
