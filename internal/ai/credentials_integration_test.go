package ai

import (
	"bytes"
	"context"
	"errors"
	"signalwatch/internal/generation"
	"testing"
)

func TestNamedCredentialsOfSameProviderRemainIndependent(t *testing.T) {
	f := newAIIntegrationFixture(t)
	ctx := context.Background()
	u := f.users[0].ID
	original, err := f.configurations.store.Read(ctx, u)
	mustAI(t, err)
	first, err := f.configurations.CreateCredential(ctx, u, "研究专用", "glm", "glm-4.7-flash", "research-key-5678")
	mustAI(t, err)
	second, err := f.configurations.CreateCredential(ctx, u, "备用", "glm", "glm-4.7-flash", "backup-key-9012")
	mustAI(t, err)
	if first.ID == second.ID || first.ID == original.Generation || first.IsDefault || second.IsDefault {
		t.Fatal("new entry overwrote an identity or default")
	}
	rows, err := f.configurations.ListCredentials(ctx, u)
	mustAI(t, err)
	if len(rows) != 3 {
		t.Fatalf("wanted three keys, got %d", len(rows))
	}
	unchanged, err := f.configurations.store.Read(ctx, u)
	mustAI(t, err)
	if unchanged.Generation != original.Generation || !bytes.Equal(unchanged.SecretCiphertext, original.SecretCiphertext) || unchanged.ConfigVersion != original.ConfigVersion {
		t.Fatal("creation changed old key or AAD")
	}
	if _, err := f.configurations.Selection(ctx, u, "glm", "glm-4.7-flash"); !errors.Is(err, ErrConfigurationConflict) {
		t.Fatal("ambiguous provider picked an arbitrary key", err)
	}
	if _, err := f.configurations.SelectionForCredential(ctx, u, first.ID, "qwen", "qwen3.8-flash"); !errors.Is(err, ErrConfigurationConflict) {
		t.Fatal("cross-provider selection allowed")
	}
	if _, err := f.configurations.SelectionForCredential(ctx, f.users[1].ID, first.ID, "glm", "glm-4.7-flash"); !errors.Is(err, ErrConfigurationRequired) {
		t.Fatal("cross-user key access allowed")
	}
	updated, err := f.configurations.UpdateCredential(ctx, u, first.ID, "研究主力", "glm-5.2", "", first.Version)
	mustAI(t, err)
	if updated.ID != first.ID || updated.Name != "研究主力" || updated.Version == first.Version {
		t.Fatal("edit lost identity/version/name")
	}
	if _, err := f.configurations.UpdateCredential(ctx, u, first.ID, "stale", "glm-5.2", "", first.Version); !errors.Is(err, ErrConfigurationConflict) {
		t.Fatal("stale edit allowed")
	}
	if err := f.configurations.Delete(CredentialIDContext(ctx, first.ID), u, first.Version); !errors.Is(err, ErrConfigurationConflict) {
		t.Fatal("stale delete allowed")
	}
	// The actual call path must decrypt this entry's saved model AAD, even when
	// the user temporarily selects a different chat model from the same vendor.
	beforeCalls := 0
	f.configurations.calls.factory = func(_ []string, provider, model, key string) (Generator, error) {
		if key != "research-key-5678" || provider != "glm" || model != "glm-4.7-flash" {
			t.Fatal("wrong credential routed")
		}
		return GeneratorFunc(func(context.Context, string, []byte, int) (generation.Result, error) {
			return generation.Result{Content: []byte(`{"type":"answer","content":"ok"}`)}, nil
		}), nil
	}
	_, err = f.configurations.GenerateForCredential(ctx, u, "glm", "glm-4.7-flash", updated.Generation, updated.Version, FeatureSubscriptionAgent, "named-run", "prompt", []byte(`{}`), func(context.Context) error { beforeCalls++; return nil }, nil)
	mustAI(t, err)
	if beforeCalls != 1 {
		t.Fatalf("call-start hook ran %d times", beforeCalls)
	}
	used, err := f.configurations.Get(CredentialIDContext(ctx, first.ID), u)
	mustAI(t, err)
	unused, err := f.configurations.Get(CredentialIDContext(ctx, second.ID), u)
	mustAI(t, err)
	if used.LastUsedAt == nil || unused.LastUsedAt != nil {
		t.Fatal("last-used time was not scoped to the actual key")
	}
	mustAI(t, f.configurations.SelectDefault(ctx, u, "glm", updated.Generation, updated.Version))
	mustAI(t, f.configurations.Delete(CredentialIDContext(ctx, second.ID), u, second.Version))
	current, err := f.configurations.Get(ctx, u)
	mustAI(t, err)
	if current.ID != first.ID || !current.Usable {
		t.Fatal("default selection/deletion affected sibling")
	}
	var defaults int64
	mustAI(t, f.db.Model(&Configuration{}).Where("user_id=? AND is_default=1", u).Count(&defaults).Error)
	if defaults != 1 {
		t.Fatal("multiple defaults")
	}
	mustAI(t, f.configurations.Delete(CredentialIDContext(ctx, first.ID), u, updated.Version))
	current, err = f.configurations.Get(ctx, u)
	mustAI(t, err)
	if current.Configured {
		t.Fatal("deleting default silently selected another credential")
	}
	if _, err := f.configurations.GenerateForCredential(ctx, u, "glm", "glm-4.7-flash", updated.Generation, updated.Version, FeatureSubscriptionAgent, "deleted-run", "prompt", nil, nil, nil); !errors.Is(err, ErrConfigurationRequired) {
		t.Fatal("deleted credential used another same-provider key")
	}
}

func TestMultipleCredentialsPreserveAADAndDefaultIsolation(t *testing.T) {
	f := newAIIntegrationFixture(t)
	ctx := context.Background()
	uid := f.users[0].ID
	before, err := f.configurations.Get(ctx, uid)
	mustAI(t, err)
	qctx := CredentialContext(ctx, "qwen")
	other, err := f.configurations.Put(qctx, uid, "qwen", "qwen3.8-flash", "qwen-key-1234", nil)
	mustAI(t, err)
	if other.IsDefault {
		t.Fatal("new provider replaced default")
	}
	unchanged, err := f.configurations.Get(ctx, uid)
	mustAI(t, err)
	if unchanged.Generation != before.Generation || unchanged.Version != before.Version || unchanged.ProviderID != "glm" {
		t.Fatal("default mutated")
	}
	// A temporary model selection must decrypt using the saved flash model.
	f.configurations.calls.factory = func(_ []string, p, m, key string) (Generator, error) {
		if p != "qwen" || m != "qwen3.8-max" || key != "qwen-key-1234" {
			t.Fatalf("wrong selection or AAD: %s %s", p, m)
		}
		return GeneratorFunc(func(context.Context, string, []byte, int) (generation.Result, error) {
			return generation.Result{Content: []byte(`{"type":"answer","content":"ok"}`)}, nil
		}), nil
	}
	_, err = f.configurations.GenerateForCredential(ctx, uid, "qwen", "qwen3.8-max", other.Generation, other.Version, FeaturePaperQA, "run", "prompt", []byte(`{}`), nil, nil)
	mustAI(t, err)
	c, err := f.configurations.Get(qctx, uid)
	mustAI(t, err)
	if c.ModelID != "qwen3.8-flash" || c.Version != other.Version {
		t.Fatal("chat changed saved AAD model")
	}
	mustAI(t, f.configurations.Delete(qctx, uid, other.Version))
	active, err := f.configurations.Active(ctx, uid)
	mustAI(t, err)
	if !active {
		t.Fatal("deleting nondefault disabled default")
	}
	_, err = f.configurations.GenerateForCredential(ctx, uid, "qwen", "qwen3.8-max", other.Generation, other.Version, FeaturePaperQA, "run", "prompt", []byte(`{}`), nil, nil)
	if !errors.Is(err, ErrConfigurationRequired) {
		t.Fatalf("deleted key remained usable: %v", err)
	}
}

func TestSelectingAndDeletingDefaultCredentialDoesNotAffectOtherKeys(t *testing.T) {
	f := newAIIntegrationFixture(t)
	ctx := context.Background()
	uid := f.users[0].ID
	old, err := f.configurations.Get(ctx, uid)
	mustAI(t, err)
	qctx := CredentialContext(ctx, "qwen")
	q, err := f.configurations.Put(qctx, uid, "qwen", "qwen3.8-flash", "qwen-key-1234", nil)
	mustAI(t, err)
	mustAI(t, f.configurations.SelectDefault(ctx, uid, "qwen", q.Generation, q.Version))
	current, err := f.configurations.Get(ctx, uid)
	mustAI(t, err)
	if current.ProviderID != "qwen" || !current.IsDefault {
		t.Fatal("default switch failed")
	}
	rows, err := f.configurations.ListCredentials(ctx, uid)
	mustAI(t, err)
	if len(rows) != 2 {
		t.Fatal("switch discarded original key")
	}
	mustAI(t, f.configurations.Delete(CredentialContext(ctx, "glm"), uid, old.Version))
	current, err = f.configurations.Get(ctx, uid)
	mustAI(t, err)
	if !current.Usable {
		t.Fatal("nondefault deletion disabled current default")
	}
	mustAI(t, f.configurations.Delete(qctx, uid, q.Version))
	current, err = f.configurations.Get(ctx, uid)
	mustAI(t, err)
	if current.Configured {
		t.Fatal("default silently fell back")
	}
	var enabled int64
	mustAI(t, f.db.Table("subscriptions").Where("user_id=? AND digest_ai_enabled=1", uid).Count(&enabled).Error)
	if enabled != 0 {
		t.Fatal("default deletion did not disable digest AI")
	}
}
