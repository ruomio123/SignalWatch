package arxiv

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
)

type doerFunc func(*http.Request) (*http.Response, error)

func (function doerFunc) Do(request *http.Request) (*http.Response, error) {
	return function(request)
}

type limiterFunc func(context.Context) error

func (function limiterFunc) Wait(ctx context.Context) error { return function(ctx) }

func TestClientFetchPageUsesCategoryOnlyDescendingQueryAndParsesStableIDs(t *testing.T) {
	fixture, err := os.ReadFile("testdata/papers.xml")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var requested *http.Request
	client, err := NewClient(
		doerFunc(func(request *http.Request) (*http.Response, error) {
			requested = request
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(string(fixture))),
				Header:     make(http.Header),
			}, nil
		}),
		limiterFunc(func(context.Context) error { return nil }),
		Config{
			Endpoint: "https://export.arxiv.org/api/query", PageSize: 100,
			MaxResponseBytes: 1 << 20, RequestAttempts: 1,
		},
	)
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	result, err := client.FetchPage(
		context.Background(),
		FetchPageRequest{Category: "cs.AI", Start: 100},
	)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if len(result.Records) != 2 || result.TotalResults != 2 || result.Requests != 1 {
		t.Fatalf("unexpected result: %+v", result)
	}
	if result.Records[0].ArXivID != "2608.00001" ||
		result.Records[0].ArXivURL != "https://arxiv.org/abs/2608.00001" ||
		result.Records[0].PDFURL != "https://arxiv.org/pdf/2608.00001" {
		t.Fatalf("unexpected v1 record: %+v", result.Records[0])
	}
	if result.Records[1].ArXivID != "2608.00002" ||
		!result.Records[1].ArXivUpdatedAt.After(result.Records[1].PublishedAt) {
		t.Fatalf("unexpected v2 record: %+v", result.Records[1])
	}
	query := requested.URL.Query().Get("search_query")
	if query != "cat:cs.AI" || strings.Contains(query, "Date") {
		t.Fatalf("unexpected arxiv query: %s", query)
	}
	if requested.URL.Query().Get("sortBy") != "lastUpdatedDate" ||
		requested.URL.Query().Get("sortOrder") != "descending" ||
		requested.URL.Query().Get("start") != "100" {
		t.Fatalf("unexpected arxiv paging and sort query: %s", requested.URL.RawQuery)
	}
	if requested.Header.Get("User-Agent") == "" {
		t.Fatal("arxiv request must identify the client")
	}
}

func TestClientClassifiesNonRetryableHTTPStatus(t *testing.T) {
	client, err := NewClient(
		doerFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusBadRequest, Body: io.NopCloser(strings.NewReader("bad"))}, nil
		}),
		limiterFunc(func(context.Context) error { return nil }),
		Config{Endpoint: "https://export.arxiv.org/api/query", PageSize: 10, MaxResponseBytes: 100, RequestAttempts: 1},
	)
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	_, err = client.FetchPage(context.Background(), FetchPageRequest{Category: "cs.AI"})
	fetchError, ok := err.(*FetchError)
	if !ok || fetchError.Retryable {
		t.Fatalf("expected permanent fetch error, got %T %v", err, err)
	}
}

func TestClientRateLimitsEveryRetryAttempt(t *testing.T) {
	httpCalls := 0
	limiterCalls := 0
	client, err := NewClient(
		doerFunc(func(*http.Request) (*http.Response, error) {
			httpCalls++
			if httpCalls == 1 {
				return &http.Response{
					StatusCode: http.StatusServiceUnavailable,
					Body:       io.NopCloser(strings.NewReader("retry")),
				}, nil
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Body: io.NopCloser(strings.NewReader(
					`<?xml version="1.0"?><feed xmlns="http://www.w3.org/2005/Atom" xmlns:opensearch="http://a9.com/-/spec/opensearch/1.1/"><opensearch:totalResults>0</opensearch:totalResults></feed>`,
				)),
			}, nil
		}),
		limiterFunc(func(context.Context) error {
			limiterCalls++
			return nil
		}),
		Config{
			Endpoint: "https://export.arxiv.org/api/query", PageSize: 10,
			MaxResponseBytes: 1 << 20, RequestAttempts: 2,
		},
	)
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	result, err := client.FetchPage(context.Background(), FetchPageRequest{Category: "cs.AI"})
	if err != nil {
		t.Fatalf("fetch after retry: %v", err)
	}
	if result.Requests != 2 || httpCalls != 2 || limiterCalls != 2 {
		t.Fatalf(
			"every HTTP attempt must reserve a rate-limit slot: result=%+v http=%d limiter=%d",
			result, httpCalls, limiterCalls,
		)
	}
}

func TestClientRejectsOversizedAndMalformedResponses(t *testing.T) {
	tests := []struct {
		name     string
		body     string
		maxBytes int64
		code     string
	}{
		{
			name: "oversized", body: strings.Repeat("x", 101), maxBytes: 100,
			code: "ARXIV_RESPONSE_TOO_LARGE",
		},
		{
			name:     "invalid entry date",
			body:     `<?xml version="1.0"?><feed xmlns="http://www.w3.org/2005/Atom" xmlns:opensearch="http://a9.com/-/spec/opensearch/1.1/" xmlns:arxiv="http://arxiv.org/schemas/atom"><opensearch:totalResults>1</opensearch:totalResults><entry><id>http://arxiv.org/abs/2608.1v1</id><updated>bad-date</updated><published>2026-08-01T00:00:00Z</published><title>title</title><summary>summary</summary><arxiv:primary_category term="cs.AI"/><category term="cs.AI"/></entry></feed>`,
			maxBytes: 1 << 20, code: "ARXIV_INVALID_ENTRY",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client, err := NewClient(
				doerFunc(func(*http.Request) (*http.Response, error) {
					return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(test.body))}, nil
				}),
				limiterFunc(func(context.Context) error { return nil }),
				Config{Endpoint: "https://export.arxiv.org/api/query", PageSize: 10, MaxResponseBytes: test.maxBytes, RequestAttempts: 1},
			)
			if err != nil {
				t.Fatalf("new client: %v", err)
			}
			_, err = client.FetchPage(context.Background(), FetchPageRequest{Category: "cs.AI"})
			var fetchError *FetchError
			if !errors.As(err, &fetchError) || fetchError.Code != test.code || fetchError.Retryable {
				t.Fatalf("expected permanent %s, got %T %v", test.code, err, err)
			}
		})
	}
}

func TestExternalIDFromURLPreservesLegacyArchivePrefix(t *testing.T) {
	identifier, err := externalIDFromURL("http://arxiv.org/abs/hep-th/9901001v2")
	if err != nil {
		t.Fatalf("parse legacy id: %v", err)
	}
	if identifier != "hep-th/9901001v2" {
		t.Fatalf("unexpected legacy id %q", identifier)
	}
}
