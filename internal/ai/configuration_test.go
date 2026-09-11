package ai

import (
	"context"
	"encoding/json"
	"errors"
	"signalwatch/internal/platform/llm"
	"strings"
	"testing"
)

func TestCredentialMaterialCannotBeMarshaled(t *testing.T) {
	const sentinel = "SENTINEL-API-KEY-NEVER-RETURN"
	raw, err := json.Marshal(Configuration{SecretCiphertext: []byte(sentinel), SecretNonce: []byte(sentinel), MasterKeyVersion: sentinel, KeyHint: "TURN"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), sentinel) {
		t.Fatalf("credential material leaked: %s", raw)
	}
	publicRaw, err := json.Marshal(testPublicConfiguration(Configuration{ProviderID: "glm", ModelID: "glm-4.7-flash", Status: ConfigurationActive, ConfigVersion: 1, KeyHint: "TURN"}, []string{"glm"}))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(publicRaw), sentinel) || !strings.Contains(string(publicRaw), "••••TURN") {
		t.Fatalf("unsafe public configuration: %s", publicRaw)
	}
}

func TestPublicConfigurationSeparatesCredentialStatusFromAvailability(t *testing.T) {
	active := Configuration{ProviderID: "deepseek", ModelID: "deepseek-flash", Status: ConfigurationActive, ConfigVersion: 1, KeyHint: "1234"}
	if got := testPublicConfiguration(active, []string{"deepseek"}); !got.Usable || got.UnusableReason != "" {
		t.Fatalf("active configuration=%+v", got)
	}
	retired := active
	retired.ModelID = "deepseek-v4-pro"
	if got := testPublicConfiguration(retired, []string{"deepseek"}); got.Usable || got.UnusableReason != "model_retired" {
		t.Fatalf("retired configuration=%+v", got)
	}
	if got := testPublicConfiguration(active, []string{"glm"}); got.Usable || got.UnusableReason != "provider_disabled" {
		t.Fatalf("disabled provider configuration=%+v", got)
	}
	invalid := active
	invalid.Status = ConfigurationInvalid
	if got := testPublicConfiguration(invalid, []string{"deepseek"}); got.Usable || got.UnusableReason != UnusableCredential {
		t.Fatalf("invalid credential configuration=%+v", got)
	}
}

func TestRetiredModelTestStopsBeforeUsageReservation(t *testing.T) {
	service := &ConfigurationService{catalog: llm.Catalog{}, enabled: []string{"deepseek"}}
	err := service.test(context.Background(), 1, "deepseek", "deepseek-v4-pro", "sentinel-key")
	if !errors.Is(err, ErrModelUnavailable) {
		t.Fatalf("retired model error=%v", err)
	}
	err = service.test(context.Background(), 1, "kimi", "kimi-k2.6", "sentinel-key")
	if !errors.Is(err, ErrProviderDisabled) {
		t.Fatalf("disabled provider error=%v", err)
	}
}

func TestValidAPIKeyRejectsControlCharactersAndExcess(t *testing.T) {
	for _, value := range []string{"short", " key-with-space", "key with space", "key-with-newline\n", "key-含中文-1234", strings.Repeat("x", 1025)} {
		if validAPIKey(value) {
			t.Fatalf("accepted unsafe key shape %q", value)
		}
	}
	if !validAPIKey("valid-api-key-1234") {
		t.Fatal("valid key rejected")
	}
}

func testPublicConfiguration(c Configuration, e []string) PublicConfiguration {
	return publicConfiguration(c, e, llm.Catalog{})
}
