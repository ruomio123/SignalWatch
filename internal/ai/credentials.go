package ai

import (
	"context"
	"errors"
	"signalwatch/internal/generation"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// A scoped configuration service reuses validation, encryption and version rules.
// The selector is internal: user IDs always come from the authenticated caller.
type credentialScopeKey struct{}
type credentialIDScopeKey struct{}
type credentialCreateScopeKey struct{}

func CredentialIDContext(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, credentialIDScopeKey{}, id)
}

func validCredentialName(name string) bool {
	return utf8.ValidString(name) && utf8.RuneCountInString(name) >= 1 && utf8.RuneCountInString(name) <= 80 && strings.IndexFunc(name, unicode.IsControl) == -1
}

func (s *ConfigurationService) CreateCredential(ctx context.Context, uid uint64, name, provider, model, key string) (PublicConfiguration, error) {
	name = strings.TrimSpace(name)
	if !validCredentialName(name) {
		return PublicConfiguration{}, ErrInvalidCredentialName
	}
	return s.putNamed(context.WithValue(ctx, credentialCreateScopeKey{}, true), uid, provider, model, key, nil, &name)
}

var ErrInvalidCredentialName = errors.New("AI_INVALID_CREDENTIAL_NAME")

func (s *ConfigurationService) UpdateCredential(ctx context.Context, uid uint64, id, name, model, key string, expected uint64) (PublicConfiguration, error) {
	name = strings.TrimSpace(name)
	if !validCredentialName(name) {
		return PublicConfiguration{}, ErrInvalidCredentialName
	}
	ctx = CredentialIDContext(ctx, id)
	row, err := s.store.Read(ctx, uid)
	if err != nil {
		return PublicConfiguration{}, err
	}
	return s.putNamed(ctx, uid, row.ProviderID, model, key, &expected, &name)
}

func CredentialContext(ctx context.Context, provider string) context.Context {
	return context.WithValue(ctx, credentialScopeKey{}, provider)
}

type credentialStore interface {
	ListCredentials(context.Context, uint64) ([]Configuration, error)
	SelectDefault(context.Context, uint64, string, string, uint64) error
}

func (s *ConfigurationService) ListCredentials(ctx context.Context, uid uint64) ([]PublicConfiguration, error) {
	store, ok := s.store.(credentialStore)
	if !ok {
		return nil, ErrProviderUnavailable
	}
	rows, err := store.ListCredentials(ctx, uid)
	if err != nil {
		return nil, err
	}
	out := make([]PublicConfiguration, 0, len(rows))
	for _, row := range rows {
		out = append(out, publicConfiguration(row, s.enabled, s.catalog))
	}
	return out, nil
}
func (s *ConfigurationService) SelectDefault(ctx context.Context, uid uint64, provider, generation string, version uint64) error {
	c, err := s.Get(CredentialIDContext(ctx, generation), uid)
	if err != nil {
		return err
	}
	if err = configurationUsabilityError(c); err != nil {
		return err
	}
	store, ok := s.store.(credentialStore)
	if !ok {
		return ErrProviderUnavailable
	}
	return store.SelectDefault(ctx, uid, provider, generation, version)
}

// Selection validates an ephemeral model independently of the stored AAD model.
func (s *ConfigurationService) Selection(ctx context.Context, uid uint64, provider, model string) (PublicConfiguration, error) {
	return s.SelectionForCredential(ctx, uid, "", provider, model)
}
func (s *ConfigurationService) SelectionForCredential(ctx context.Context, uid uint64, id, provider, model string) (PublicConfiguration, error) {
	scoped := CredentialContext(ctx, provider)
	if id != "" {
		scoped = CredentialIDContext(ctx, id)
	}
	c, err := s.Get(scoped, uid)
	if err != nil {
		return c, err
	}
	if !c.Configured {
		return c, ErrConfigurationRequired
	}
	if c.ProviderID != provider {
		return c, ErrConfigurationConflict
	}
	if c.Status != ConfigurationActive {
		return c, ErrConfigurationInvalid
	}
	if !s.catalog.ValidateSelection(s.enabled, provider, model) {
		return c, ErrModelUnavailable
	}
	return c, nil
}
func (s *ConfigurationService) GenerateForCredential(ctx context.Context, uid uint64, provider, model, gen string, version uint64, feature, requestID, system string, input []byte, before func(context.Context) error, validate func(generation.Result) error) (generation.Result, error) {
	scoped := CredentialIDContext(ctx, gen)
	check := func(c context.Context) error {
		row, err := s.SelectionForCredential(c, uid, gen, provider, model)
		if err != nil {
			return err
		}
		if row.Generation != gen || row.Version != version {
			return ErrConfigurationConflict
		}
		return nil
	}
	if err := check(ctx); err != nil {
		return generation.Result{}, err
	}
	// Decrypt with the saved model, never the requested chat model.
	row, key, err := s.loadCurrent(scoped, uid)
	if err != nil {
		return generation.Result{}, err
	}
	defer clear(key)
	if row.Generation != gen || row.ConfigVersion != version {
		return generation.Result{}, ErrConfigurationConflict
	}
	beforeStart := func(c context.Context) error {
		if err := check(c); err != nil {
			return err
		}
		if before != nil {
			return before(c)
		}
		return nil
	}
	result, err := s.calls.Run(ctx, CallRequest{UserID: uid, Provider: provider, Model: model, Key: string(key), Generation: gen, Version: version, Feature: feature, RequestID: requestID, System: system, Input: input, MaxTokens: 4096, BeforeStart: beforeStart, Validate: validate, Commit: check})
	if result.CallID != "" {
		mark, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = s.store.MarkUsed(mark, uid, version, s.now().UTC())
	}
	var callErr *CallError
	if errors.As(err, &callErr) && callErr.Code == "credential_rejected" {
		_ = s.store.MarkInvalid(scoped, uid, version)
	}
	return result, err
}
