package paper

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"
)

type queryRepositoryStub struct {
	total      int64
	rows       []QueryResult
	row        QueryResult
	countErr   error
	listErr    error
	getErr     error
	userID     uint64
	filter     QueryFilter
	offset     int
	limit      int
	getPaperID uint64
}

func (stub *queryRepositoryStub) CountMatched(
	_ context.Context, userID uint64, filter QueryFilter,
) (int64, error) {
	stub.userID, stub.filter = userID, filter
	return stub.total, stub.countErr
}

func (stub *queryRepositoryStub) ListMatched(
	_ context.Context, _ uint64, _ QueryFilter, offset, limit int,
) ([]QueryResult, error) {
	stub.offset, stub.limit = offset, limit
	return stub.rows, stub.listErr
}

func (stub *queryRepositoryStub) GetMatched(
	_ context.Context, userID, paperID uint64,
) (QueryResult, error) {
	stub.userID, stub.getPaperID = userID, paperID
	return stub.row, stub.getErr
}

func TestQueryServiceListsDeduplicatedPublicPapers(t *testing.T) {
	matchedAt := time.Date(2026, 9, 8, 2, 0, 0, 0, time.UTC)
	subscriptionID := uint64(9)
	repository := &queryRepositoryStub{
		total: 21,
		rows: []QueryResult{{
			Paper: Paper{
				ID: 7, SourceID: 3, ArXivID: "2609.00007", Title: "Paper",
				Abstract: "Abstract", AuthorsJSON: json.RawMessage(`["Ada"]`),
				CategoriesJSON: json.RawMessage(`["cs.AI","cs.LG"]`),
			},
			Matches: []MatchRow{{
				SubscriptionID: 9, SubscriptionName: "Agents",
				SubscriptionCategory: "cs.AI", SubscriptionEnabled: true,
				MatchedKeywordsJSON: json.RawMessage(`["agent"]`), MatchedAt: matchedAt,
			}},
		}},
	}
	result, err := NewQueryService(repository).List(context.Background(), 42, QueryInput{
		Page: 2, PageSize: 10, Filter: QueryFilter{SubscriptionID: &subscriptionID},
	})
	if err != nil {
		t.Fatalf("list papers: %v", err)
	}
	if result.Total != 21 || result.Page != 2 || result.PageSize != 10 ||
		repository.offset != 10 || repository.limit != 10 || repository.userID != 42 ||
		repository.filter.SubscriptionID == nil || *repository.filter.SubscriptionID != 9 {
		t.Fatalf("unexpected query result=%+v repository=%+v", result, repository)
	}
	if len(result.Items) != 1 || !reflect.DeepEqual(result.Items[0].Authors, []string{"Ada"}) ||
		!reflect.DeepEqual(result.Items[0].Categories, []string{"cs.AI", "cs.LG"}) ||
		len(result.Items[0].Matches) != 1 ||
		!reflect.DeepEqual(result.Items[0].Matches[0].MatchedKeywords, []string{"agent"}) ||
		!result.Items[0].Matches[0].SubscriptionActive {
		t.Fatalf("unexpected public paper: %+v", result.Items)
	}
}

func TestQueryServiceGetPreservesInactiveMatchHistory(t *testing.T) {
	deletedAt := time.Now().UTC()
	repository := &queryRepositoryStub{row: QueryResult{
		Paper: Paper{
			ID: 4, AuthorsJSON: json.RawMessage(`[]`), CategoriesJSON: json.RawMessage(`["cs.AI"]`),
		},
		Matches: []MatchRow{{
			SubscriptionID: 2, SubscriptionName: "Deleted", SubscriptionCategory: "cs.AI",
			SubscriptionEnabled: true, SubscriptionDeletedAt: &deletedAt,
			MatchedKeywordsJSON: json.RawMessage(`[]`),
		}},
	}}
	result, err := NewQueryService(repository).Get(context.Background(), 8, 4)
	if err != nil {
		t.Fatalf("get paper: %v", err)
	}
	if repository.userID != 8 || repository.getPaperID != 4 ||
		len(result.Matches) != 1 || result.Matches[0].SubscriptionActive {
		t.Fatalf("unexpected historical paper: %+v", result)
	}
}

func TestQueryServiceValidatesInputsAndStoredJSON(t *testing.T) {
	service := NewQueryService(&queryRepositoryStub{})
	for _, input := range []QueryInput{
		{Page: 0, PageSize: 20},
		{Page: 1, PageSize: 0},
		{Page: 1, PageSize: MaxQueryPageSize + 1},
	} {
		if _, err := service.List(context.Background(), 1, input); !errors.Is(err, ErrInvalidQueryPagination) {
			t.Fatalf("expected pagination error for %+v, got %v", input, err)
		}
	}
	zero := uint64(0)
	if _, err := service.List(context.Background(), 1, QueryInput{
		Page: 1, PageSize: 20, Filter: QueryFilter{SubscriptionID: &zero},
	}); !errors.Is(err, ErrInvalidQueryFilter) {
		t.Fatalf("expected filter error, got %v", err)
	}
	malformed := NewQueryService(&queryRepositoryStub{row: QueryResult{
		Paper: Paper{ID: 1, AuthorsJSON: json.RawMessage(`null`), CategoriesJSON: json.RawMessage(`[]`)},
	}})
	if _, err := malformed.Get(context.Background(), 1, 1); !errors.Is(err, ErrInvalidQueryData) {
		t.Fatalf("expected stored JSON error, got %v", err)
	}
}
