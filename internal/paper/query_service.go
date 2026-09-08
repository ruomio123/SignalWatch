package paper

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
)

var (
	ErrInvalidQueryUser       = errors.New("invalid paper query user")
	ErrInvalidQueryID         = errors.New("invalid paper query id")
	ErrInvalidQueryPagination = errors.New("invalid paper query pagination")
	ErrInvalidQueryFilter     = errors.New("invalid paper query filter")
	ErrInvalidQueryData       = errors.New("invalid stored paper query data")
)

type QueryService struct {
	repository QueryRepository
}

func NewQueryService(repository QueryRepository) *QueryService {
	return &QueryService{repository: repository}
}

func (service *QueryService) List(
	ctx context.Context,
	userID uint64,
	input QueryInput,
) (QueryPage, error) {
	if userID == 0 {
		return QueryPage{}, ErrInvalidQueryUser
	}
	if input.Page < 1 || input.PageSize < 1 || input.PageSize > MaxQueryPageSize ||
		input.Page-1 > math.MaxInt/input.PageSize {
		return QueryPage{}, ErrInvalidQueryPagination
	}
	if input.Filter.SubscriptionID != nil && *input.Filter.SubscriptionID == 0 {
		return QueryPage{}, ErrInvalidQueryFilter
	}
	total, err := service.repository.CountMatched(ctx, userID, input.Filter)
	if err != nil {
		return QueryPage{}, err
	}
	rows, err := service.repository.ListMatched(
		ctx, userID, input.Filter, (input.Page-1)*input.PageSize, input.PageSize,
	)
	if err != nil {
		return QueryPage{}, err
	}
	items, err := publicPapers(rows)
	if err != nil {
		return QueryPage{}, err
	}
	return QueryPage{Items: items, Page: input.Page, PageSize: input.PageSize, Total: total}, nil
}

func (service *QueryService) Get(
	ctx context.Context,
	userID uint64,
	paperID uint64,
) (PublicPaper, error) {
	if userID == 0 {
		return PublicPaper{}, ErrInvalidQueryUser
	}
	if paperID == 0 {
		return PublicPaper{}, ErrInvalidQueryID
	}
	row, err := service.repository.GetMatched(ctx, userID, paperID)
	if err != nil {
		return PublicPaper{}, err
	}
	return publicPaper(row)
}

func publicPapers(rows []QueryResult) ([]PublicPaper, error) {
	items := make([]PublicPaper, 0, len(rows))
	for _, row := range rows {
		item, err := publicPaper(row)
		if err != nil {
			return nil, fmt.Errorf("build public paper %d: %w", row.Paper.ID, err)
		}
		items = append(items, item)
	}
	return items, nil
}

func publicPaper(row QueryResult) (PublicPaper, error) {
	authors, err := decodeQueryStrings(row.Paper.AuthorsJSON, "authors")
	if err != nil {
		return PublicPaper{}, err
	}
	categories, err := decodeQueryStrings(row.Paper.CategoriesJSON, "categories")
	if err != nil {
		return PublicPaper{}, err
	}
	matches := make([]PublicMatch, 0, len(row.Matches))
	for _, match := range row.Matches {
		keywords, err := decodeQueryStrings(match.MatchedKeywordsJSON, "matched keywords")
		if err != nil {
			return PublicPaper{}, err
		}
		matches = append(matches, PublicMatch{
			SubscriptionID: match.SubscriptionID, SubscriptionName: match.SubscriptionName,
			Category:           match.SubscriptionCategory,
			SubscriptionActive: match.SubscriptionEnabled && match.SubscriptionDeletedAt == nil,
			MatchedKeywords:    keywords, MatchedAt: match.MatchedAt,
		})
	}
	return PublicPaper{
		ID: row.Paper.ID, SourceID: row.Paper.SourceID, ArXivID: row.Paper.ArXivID,
		Title: row.Paper.Title, Abstract: row.Paper.Abstract,
		Authors: authors, Categories: categories,
		PublishedAt: row.Paper.PublishedAt, ArXivUpdatedAt: row.Paper.ArXivUpdatedAt,
		ArXivURL: row.Paper.ArXivURL, PDFURL: row.Paper.PDFURL,
		FirstSeenAt: row.Paper.FirstSeenAt, Matches: matches,
	}, nil
}

func decodeQueryStrings(raw json.RawMessage, field string) ([]string, error) {
	var values []string
	if err := json.Unmarshal(raw, &values); err != nil || values == nil {
		return nil, fmt.Errorf("%w: %s must be an array", ErrInvalidQueryData, field)
	}
	return values, nil
}
