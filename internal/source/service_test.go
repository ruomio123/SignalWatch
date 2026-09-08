package source

import (
	"context"
	"errors"
	"testing"
)

func TestServiceListBuildsOnlyPublicSourceData(t *testing.T) {
	endpoint := "https://secret.example/query"
	repository := sourceRepositoryStub{
		list: func(context.Context) ([]Source, error) {
			return []Source{{
				ID: 1, SourceKey: "arxiv", Kind: KindArXiv, Name: "arXiv",
				Endpoint:   &endpoint,
				ConfigJSON: []byte(`{"allowed_categories":["cs.AI","cs.CL"],"credential":"secret"}`),
			}}, nil
		},
	}

	result, err := NewService(repository).List(context.Background())
	if err != nil {
		t.Fatalf("list sources: %v", err)
	}
	if len(result) != 1 {
		t.Fatalf("expected one source, got %d", len(result))
	}
	got := result[0]
	if got.ID != 1 || got.SourceKey != "arxiv" || got.Kind != KindArXiv || got.Name != "arXiv" {
		t.Fatalf("unexpected public source %+v", got)
	}
	if len(got.RuleTypes) != 2 || len(got.AllowedCategories) != 2 {
		t.Fatalf("expected public capabilities, got %+v", got)
	}
}

func TestServiceGetRejectsZeroAndTreatsDisabledAsNotFound(t *testing.T) {
	calls := 0
	repository := sourceRepositoryStub{
		get: func(context.Context, uint64) (Source, error) {
			calls++
			return Source{}, ErrNotFound
		},
	}
	service := NewService(repository)

	if _, err := service.Get(context.Background(), 0); !errors.Is(err, ErrInvalidID) {
		t.Fatalf("expected invalid ID, got %v", err)
	}
	if calls != 0 {
		t.Fatal("zero ID must be rejected before repository access")
	}
	if _, err := service.Get(context.Background(), 2); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected disabled/missing source to be not found, got %v", err)
	}
}

func TestServiceRejectsMalformedInternalConfig(t *testing.T) {
	repository := sourceRepositoryStub{
		get: func(context.Context, uint64) (Source, error) {
			return Source{ID: 1, ConfigJSON: []byte(`{`)}, nil
		},
	}
	if _, err := NewService(repository).Get(context.Background(), 1); err == nil {
		t.Fatal("expected malformed internal source config to fail")
	}
}

type sourceRepositoryStub struct {
	list func(context.Context) ([]Source, error)
	get  func(context.Context, uint64) (Source, error)
}

func (stub sourceRepositoryStub) ListEnabled(ctx context.Context) ([]Source, error) {
	if stub.list == nil {
		return nil, nil
	}
	return stub.list(ctx)
}

func (stub sourceRepositoryStub) FindEnabledByID(ctx context.Context, id uint64) (Source, error) {
	return stub.get(ctx, id)
}
