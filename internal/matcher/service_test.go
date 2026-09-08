package matcher

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"signalwatch/internal/paper"
)

type repositoryStub struct {
	stored      paper.Paper
	candidates  []Candidate
	inserted    []paper.SubscriptionPaper
	insertCount int64
	findErr     error
	listErr     error
	insertErr   error
	sourceID    uint64
	categories  []string
	firstSeen   time.Time
}

func (stub *repositoryStub) FindPaper(context.Context, uint64) (paper.Paper, error) {
	return stub.stored, stub.findErr
}

func (stub *repositoryStub) ListCandidates(
	_ context.Context,
	sourceID uint64,
	categories []string,
	firstSeen time.Time,
) ([]Candidate, error) {
	stub.sourceID = sourceID
	stub.categories = append([]string(nil), categories...)
	stub.firstSeen = firstSeen
	return stub.candidates, stub.listErr
}

func (stub *repositoryStub) InsertMatches(
	_ context.Context,
	matches []paper.SubscriptionPaper,
) (int64, error) {
	stub.inserted = append([]paper.SubscriptionPaper(nil), matches...)
	if stub.insertCount != 0 {
		return stub.insertCount, stub.insertErr
	}
	return int64(len(matches)), stub.insertErr
}

func TestServiceMatchesCategoryKeywordOrAndFromNow(t *testing.T) {
	firstSeen := time.Date(2026, 9, 8, 1, 0, 0, 0, time.UTC)
	matchedAt := firstSeen.Add(time.Hour)
	repository := &repositoryStub{
		stored: paper.Paper{
			ID: 7, SourceID: 3,
			Title: "  Tool\nUse for AGENTS ", Abstract: "Retrieval augmented generation.",
			AuthorsJSON:    json.RawMessage(`["keyword-only-in-author"]`),
			CategoriesJSON: json.RawMessage(`["cs.LG","cs.AI","cs.AI"]`),
			FirstSeenAt:    firstSeen,
		},
		candidates: []Candidate{
			{ID: 10, Category: "cs.AI", KeywordsJSON: []byte(`["tool   use","RAG","absent"]`), CreatedAt: firstSeen},
			{ID: 11, Category: "cs.LG", KeywordsJSON: []byte(`[]`), CreatedAt: firstSeen.Add(-time.Hour)},
			{ID: 12, Category: "cs.AI", KeywordsJSON: []byte(`["absent","AGENTS"]`), CreatedAt: firstSeen},
			{ID: 13, Category: "cs.AI", KeywordsJSON: []byte(`["keyword-only-in-author"]`), CreatedAt: firstSeen},
			{ID: 14, Category: "cs.AI", KeywordsJSON: []byte(`[]`), CreatedAt: firstSeen.Add(time.Nanosecond)},
			{ID: 15, Category: "cs.CV", KeywordsJSON: []byte(`[]`), CreatedAt: firstSeen},
		},
	}
	service, err := NewService(repository, func() time.Time { return matchedAt })
	if err != nil {
		t.Fatalf("new matcher: %v", err)
	}

	result, err := service.Match(context.Background(), 7)
	if err != nil {
		t.Fatalf("match paper: %v", err)
	}
	if result.Candidates != 6 || result.Matched != 3 || result.Inserted != 3 {
		t.Fatalf("unexpected result: %+v", result)
	}
	if repository.sourceID != 3 || !repository.firstSeen.Equal(firstSeen) ||
		!reflect.DeepEqual(repository.categories, []string{"cs.LG", "cs.AI"}) {
		t.Fatalf("unexpected candidate query: source=%d categories=%v first_seen=%v",
			repository.sourceID, repository.categories, repository.firstSeen)
	}
	if len(repository.inserted) != 3 {
		t.Fatalf("unexpected matches: %+v", repository.inserted)
	}
	wantSubscriptions := []uint64{10, 11, 12}
	wantKeywords := [][]string{{"tool use"}, {}, {"AGENTS"}}
	for index, match := range repository.inserted {
		if match.SubscriptionID != wantSubscriptions[index] || match.PaperID != 7 ||
			!match.MatchedAt.Equal(matchedAt) {
			t.Fatalf("unexpected match %d: %+v", index, match)
		}
		var keywords []string
		if err := json.Unmarshal(match.MatchedKeywordsJSON, &keywords); err != nil {
			t.Fatalf("decode matched keywords: %v", err)
		}
		if !reflect.DeepEqual(keywords, wantKeywords[index]) {
			t.Fatalf("match %d keywords=%v want=%v", index, keywords, wantKeywords[index])
		}
	}
}

func TestServiceRejectsInvalidStoredJSONBeforeInsert(t *testing.T) {
	firstSeen := time.Now().UTC()
	for _, test := range []struct {
		name       string
		categories json.RawMessage
		candidates []Candidate
	}{
		{name: "paper categories are null", categories: json.RawMessage(`null`)},
		{name: "paper category is blank", categories: json.RawMessage(`[" "]`)},
		{
			name: "subscription keyword is null", categories: json.RawMessage(`["cs.AI"]`),
			candidates: []Candidate{{ID: 1, Category: "cs.AI", KeywordsJSON: []byte(`null`), CreatedAt: firstSeen}},
		},
		{
			name: "subscription keyword is blank", categories: json.RawMessage(`["cs.AI"]`),
			candidates: []Candidate{{ID: 1, Category: "cs.AI", KeywordsJSON: []byte(`[""]`), CreatedAt: firstSeen}},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			repository := &repositoryStub{
				stored:     paper.Paper{ID: 1, SourceID: 1, CategoriesJSON: test.categories, FirstSeenAt: firstSeen},
				candidates: test.candidates,
			}
			service, err := NewService(repository, time.Now)
			if err != nil {
				t.Fatalf("new matcher: %v", err)
			}
			_, err = service.Match(context.Background(), 1)
			if !errors.Is(err, ErrInvalidStoredJSON) {
				t.Fatalf("expected invalid stored JSON, got %v", err)
			}
			if repository.inserted != nil {
				t.Fatalf("invalid data must not be inserted: %+v", repository.inserted)
			}
		})
	}
}

func TestServiceRejectsZeroPaperID(t *testing.T) {
	service, err := NewService(&repositoryStub{}, time.Now)
	if err != nil {
		t.Fatalf("new matcher: %v", err)
	}
	if _, err := service.Match(context.Background(), 0); !errors.Is(err, ErrInvalidPaperID) {
		t.Fatalf("expected invalid paper ID, got %v", err)
	}
}
