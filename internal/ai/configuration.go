package ai

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"signalwatch/internal/generation"
	"signalwatch/internal/platform/secret"
)

const (
	ConfigurationActive  = "active"
	ConfigurationInvalid = "invalid"
	UnusableCredential   = "credential_invalid"
	FeatureDigest        = "digest"
	FeaturePaper         = "paper"
	FeatureConfigTest    = "config_test"
)

var (
	ErrConfigurationRequired = errors.New("AI_CONFIGURATION_REQUIRED")
	ErrConfigurationInvalid  = errors.New("AI_CONFIGURATION_INVALID")
	ErrModelUnavailable      = errors.New("AI_MODEL_UNAVAILABLE")
	ErrProviderDisabled      = errors.New("AI_PROVIDER_DISABLED")
	ErrUsageLimit            = errors.New("AI_DAILY_LIMIT_REACHED")
	ErrCallInProgress        = errors.New("AI_CALL_IN_PROGRESS")
	ErrConfigurationConflict = errors.New("AI_CONFIGURATION_VERSION_CONFLICT")
	ErrProviderUnavailable   = errors.New("AI_PROVIDER_UNAVAILABLE")
	ErrInvalidSecret         = errors.New("invalid AI API key")
)

type Configuration struct {
	Generation       string     `gorm:"column:generation"`
	UserID           uint64     `gorm:"column:user_id;primaryKey"`
	ProviderID       string     `gorm:"column:provider_id"`
	ModelID          string     `gorm:"column:model_id"`
	Status           string     `gorm:"column:status"`
	ConfigVersion    uint64     `gorm:"column:config_version"`
	KeyHint          string     `gorm:"column:key_hint"`
	SecretCiphertext []byte     `gorm:"column:secret_ciphertext" json:"-"`
	SecretNonce      []byte     `gorm:"column:secret_nonce" json:"-"`
	MasterKeyVersion string     `gorm:"column:master_key_version" json:"-"`
	LastTestedAt     *time.Time `gorm:"column:last_tested_at"`
	LastUsedAt       *time.Time `gorm:"column:last_used_at"`
	CreatedAt        time.Time  `gorm:"column:created_at"`
	UpdatedAt        time.Time  `gorm:"column:updated_at"`
}

func (Configuration) TableName() string { return "user_ai_configurations" }

type PublicConfiguration struct {
	Generation     string     `json:"generation,omitempty"`
	Configured     bool       `json:"configured"`
	Usable         bool       `json:"usable"`
	UnusableReason string     `json:"unusable_reason,omitempty"`
	ProviderID     string     `json:"provider,omitempty"`
	ModelID        string     `json:"model,omitempty"`
	MaskedKey      string     `json:"masked_key,omitempty"`
	Status         string     `json:"status,omitempty"`
	Version        uint64     `json:"version,omitempty"`
	LastTestedAt   *time.Time `json:"last_tested_at,omitempty"`
	LastUsedAt     *time.Time `json:"last_used_at,omitempty"`
	CreatedAt      *time.Time `json:"created_at,omitempty"`
	UpdatedAt      *time.Time `json:"updated_at,omitempty"`
}

func publicConfiguration(c Configuration, enabled []string, catalog ProviderCatalog) PublicConfiguration {
	created, updated := c.CreatedAt, c.UpdatedAt
	result := PublicConfiguration{Generation: c.Generation, Configured: true, ProviderID: c.ProviderID, ModelID: c.ModelID,
		MaskedKey: "••••" + c.KeyHint, Status: c.Status, Version: c.ConfigVersion,
		LastTestedAt: c.LastTestedAt, LastUsedAt: c.LastUsedAt, CreatedAt: &created, UpdatedAt: &updated}
	if available, reason := catalog.SelectionAvailability(enabled, c.ProviderID, c.ModelID); !available {
		result.UnusableReason = reason
	} else if c.Status != ConfigurationActive {
		result.UnusableReason = UnusableCredential
	} else {
		result.Usable = true
	}
	return result
}

func configurationUsabilityError(configuration PublicConfiguration) error {
	if !configuration.Configured {
		return ErrConfigurationRequired
	}
	if configuration.Usable {
		return nil
	}
	if configuration.UnusableReason == UnusableCredential {
		return ErrConfigurationInvalid
	}
	if configuration.UnusableReason == generation.UnavailableProviderDisabled {
		return ErrProviderDisabled
	}
	return ErrModelUnavailable
}

type Generator interface {
	GenerateLimit(context.Context, string, []byte, int) (generation.Result, error)
}

type ClientFactory func([]string, string, string, string) (Generator, error)

type ProviderCatalog interface {
	Providers([]string) []generation.Provider
	SelectionAvailability([]string, string, string) (bool, string)
	ValidateSelection([]string, string, string) bool
}
type CredentialCipher interface {
	Encrypt([]byte, string) ([]byte, []byte, string, error)
	Decrypt([]byte, []byte, string, string) ([]byte, error)
}
type ConfigurationService struct {
	catalog ProviderCatalog
	store   ConfigurationStore
	keyring CredentialCipher
	enabled []string
	now     func() time.Time
	calls   *CallRunner
}

func NewConfigurationService(store ConfigurationStore, keyring CredentialCipher, enabled []string, now func() time.Time, calls *CallRunner, catalog ProviderCatalog) *ConfigurationService {
	if now == nil {
		now = time.Now
	}
	if store == nil || keyring == nil || calls == nil || catalog == nil {
		panic("invalid AI configuration dependencies")
	}
	return &ConfigurationService{store: store, keyring: keyring, enabled: append([]string(nil), enabled...), now: now,
		calls: calls, catalog: catalog}
}

func (s *ConfigurationService) Providers() []generation.Provider {
	return s.catalog.Providers(s.enabled)
}

func (s *ConfigurationService) Get(ctx context.Context, userID uint64) (PublicConfiguration, error) {
	row, err := s.store.Read(ctx, userID)
	if errors.Is(err, ErrConfigurationRequired) {
		return PublicConfiguration{Configured: false}, nil
	}
	if err != nil {
		return PublicConfiguration{}, err
	}
	return publicConfiguration(row, s.enabled, s.catalog), nil
}

func (s *ConfigurationService) Active(ctx context.Context, userID uint64) (bool, error) {
	c, err := s.Get(ctx, userID)
	return c.Usable, err
}

type UsableConfiguration struct {
	UserID, Version uint64
	Provider, Model string
	Secret          []byte
}

func (s *ConfigurationService) ForUse(ctx context.Context, userID, version uint64) (UsableConfiguration, error) {
	row, err := s.store.Read(ctx, userID)
	if err != nil {
		return UsableConfiguration{}, err
	}
	if row.Status != ConfigurationActive || (version != 0 && row.ConfigVersion != version) {
		return UsableConfiguration{}, ErrConfigurationRequired
	}
	if !s.catalog.ValidateSelection(s.enabled, row.ProviderID, row.ModelID) {
		return UsableConfiguration{}, ErrModelUnavailable
	}
	plain, err := s.keyring.Decrypt(row.SecretCiphertext, row.SecretNonce, row.MasterKeyVersion,
		secret.AAD(row.UserID, row.ProviderID, row.ModelID, row.ConfigVersion))
	if err != nil {
		return UsableConfiguration{}, ErrConfigurationInvalid
	}
	return UsableConfiguration{UserID: row.UserID, Version: row.ConfigVersion, Provider: row.ProviderID, Model: row.ModelID, Secret: plain}, nil
}

func validAPIKey(value string) bool {
	if value != strings.TrimSpace(value) || len(value) < 8 || len(value) > 1024 {
		return false
	}
	for i := range len(value) {
		if value[i] < 0x21 || value[i] > 0x7e {
			return false
		}
	}
	return true
}

func (s *ConfigurationService) Put(ctx context.Context, userID uint64, provider, model, apiKey string, expected *uint64) (PublicConfiguration, error) {
	if !s.catalog.ValidateSelection(s.enabled, provider, model) {
		return PublicConfiguration{}, ErrProviderDisabled
	}
	before, beforeErr := s.store.Read(ctx, userID)
	if beforeErr == nil && (expected == nil || *expected != before.ConfigVersion) {
		return PublicConfiguration{}, ErrConfigurationConflict
	}
	if errors.Is(beforeErr, ErrConfigurationRequired) && expected != nil {
		return PublicConfiguration{}, ErrConfigurationConflict
	}
	if beforeErr != nil && !errors.Is(beforeErr, ErrConfigurationRequired) {
		return PublicConfiguration{}, beforeErr
	}
	if apiKey == "" && beforeErr == nil && before.ProviderID == provider {
		key, err := s.keyring.Decrypt(before.SecretCiphertext, before.SecretNonce, before.MasterKeyVersion, secret.AAD(userID, before.ProviderID, before.ModelID, before.ConfigVersion))
		if err != nil {
			return PublicConfiguration{}, ErrConfigurationInvalid
		}
		defer clear(key)
		apiKey = string(key)
	}
	if !validAPIKey(apiKey) {
		return PublicConfiguration{}, ErrInvalidSecret
	}
	var output PublicConfiguration
	err := s.verify(ctx, userID, provider, model, apiKey, before.Generation, before.ConfigVersion, func(commit context.Context) error {
		var err error
		output, err = s.saveVerified(commit, userID, provider, model, apiKey, expected)
		return err
	})
	return output, err
}
func (s *ConfigurationService) saveVerified(ctx context.Context, userID uint64, provider, model, apiKey string, expected *uint64) (PublicConfiguration, error) {
	now := s.now().UTC()
	saved, err := s.store.Mutate(ctx, userID, expected, func(current Configuration, revision uint64) (Configuration, error) {
		saved := current
		if current.Generation == "" {
			saved.Generation = rand.Text()
			saved.CreatedAt = now
		}
		saved.ConfigVersion = revision
		ciphertext, nonce, masterVersion, err := s.keyring.Encrypt([]byte(apiKey), secret.AAD(userID, provider, model, revision))
		if err != nil {
			return Configuration{}, err
		}
		saved.UserID, saved.ProviderID, saved.ModelID = userID, provider, model
		saved.Status, saved.KeyHint = ConfigurationActive, apiKey[len(apiKey)-4:]
		saved.SecretCiphertext, saved.SecretNonce, saved.MasterKeyVersion = ciphertext, nonce, masterVersion
		saved.LastTestedAt, saved.UpdatedAt = &now, now
		return saved, nil
	})
	if err != nil {
		return PublicConfiguration{}, err
	}
	return publicConfiguration(saved, s.enabled, s.catalog), nil
}

func (s *ConfigurationService) ChangeModel(ctx context.Context, userID, expected uint64, model string) (PublicConfiguration, error) {
	current, err := s.store.Read(ctx, userID)
	if err != nil {
		return PublicConfiguration{}, err
	}
	return s.Put(ctx, userID, current.ProviderID, model, "", &expected)
}
func (s *ConfigurationService) RotateSecret(ctx context.Context, userID, expected uint64, apiKey string) (PublicConfiguration, error) {
	if !validAPIKey(apiKey) {
		return PublicConfiguration{}, ErrInvalidSecret
	}
	current, err := s.store.Read(ctx, userID)
	if err != nil {
		return PublicConfiguration{}, err
	}
	return s.Put(ctx, userID, current.ProviderID, current.ModelID, apiKey, &expected)
}

func (s *ConfigurationService) loadExpected(ctx context.Context, userID, expected uint64) (Configuration, []byte, error) {
	row, err := s.store.Read(ctx, userID)
	if err != nil {
		return row, nil, err
	}
	if expected == 0 || row.ConfigVersion != expected {
		return row, nil, ErrConfigurationConflict
	}
	plain, err := s.keyring.Decrypt(row.SecretCiphertext, row.SecretNonce, row.MasterKeyVersion, secret.AAD(userID, row.ProviderID, row.ModelID, row.ConfigVersion))
	if err != nil {
		return row, nil, ErrConfigurationInvalid
	}
	return row, plain, nil
}

func (s *ConfigurationService) TestSaved(ctx context.Context, userID uint64) (PublicConfiguration, error) {
	row, key, err := s.loadCurrent(ctx, userID)
	if err != nil {
		return PublicConfiguration{}, err
	}
	defer clear(key)
	err = s.verify(ctx, userID, row.ProviderID, row.ModelID, string(key), row.Generation, row.ConfigVersion, func(commit context.Context) error {
		return s.store.MarkTested(commit, userID, row.ConfigVersion, s.now().UTC())
	})
	if err != nil {
		var failed *CallError
		if errors.As(err, &failed) && failed.Code == "credential_rejected" {
			mark, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			markErr := s.store.MarkInvalid(mark, userID, row.ConfigVersion)
			cancel()
			if markErr != nil {
				return PublicConfiguration{}, &CallError{Code: "storage_failed", CallID: failed.CallID}
			}
		}
		return PublicConfiguration{}, err
	}
	return s.Get(ctx, userID)
}

func (s *ConfigurationService) test(ctx context.Context, userID uint64, provider, model, apiKey string) error {
	return s.verify(ctx, userID, provider, model, apiKey, "", 0, nil)
}
func (s *ConfigurationService) verify(ctx context.Context, userID uint64, provider, model, apiKey, generationID string, version uint64, commit func(context.Context) error) error {
	available, reason := s.catalog.SelectionAvailability(s.enabled, provider, model)
	if !available {
		if reason == generation.UnavailableProviderDisabled {
			return ErrProviderDisabled
		}
		return ErrModelUnavailable
	}
	_, err := s.calls.Run(ctx, CallRequest{UserID: userID, Feature: FeatureConfigTest, Provider: provider, Model: model, Key: apiKey, Generation: generationID, Version: version,
		System: `Return exactly this JSON object: {"ok":true}. No markdown.`, Input: []byte(`{"test":true}`), MaxTokens: 1024,
		Validate: func(result generation.Result) error {
			var value struct {
				OK bool `json:"ok"`
			}
			if json.Unmarshal(result.Content, &value) != nil || !value.OK {
				return &generation.Failure{Code: "invalid_output"}
			}
			return nil
		}, Commit: commit})
	return err
}

func (s *ConfigurationService) Delete(ctx context.Context, userID, expected uint64) error {
	return s.store.Delete(ctx, userID, expected, s.now().UTC())
}

type UsageRow struct {
	Day          string `json:"day" gorm:"column:day"`
	Feature      string `json:"feature" gorm:"column:feature"`
	Calls        int    `json:"calls"`
	Succeeded    int    `json:"succeeded"`
	Failed       int    `json:"failed"`
	Unknown      int    `json:"unknown"`
	InputTokens  int64  `json:"input_tokens"`
	OutputTokens int64  `json:"output_tokens"`
	UsageMissing int    `json:"usage_missing" gorm:"column:usage_missing"`
}

func (s *ConfigurationService) loadCurrent(ctx context.Context, u uint64) (Configuration, []byte, error) {
	row, err := s.store.Read(ctx, u)
	if err != nil {
		return row, nil, err
	}
	key, err := s.keyring.Decrypt(row.SecretCiphertext, row.SecretNonce, row.MasterKeyVersion, secret.AAD(u, row.ProviderID, row.ModelID, row.ConfigVersion))
	if err != nil {
		return row, nil, ErrConfigurationInvalid
	}
	return row, key, nil
}
