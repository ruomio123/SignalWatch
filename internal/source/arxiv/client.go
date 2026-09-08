package arxiv

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"signalwatch/internal/paper"
)

const userAgent = "SignalWatch/0.1 (subscription-driven research monitor)"

var versionPattern = regexp.MustCompile(`^(.*)v([1-9][0-9]*)$`)
var categoryPattern = regexp.MustCompile(`^[A-Za-z0-9.-]+$`)

type HTTPDoer interface {
	Do(request *http.Request) (*http.Response, error)
}

type RateLimiter interface {
	Wait(ctx context.Context) error
}

type Config struct {
	Endpoint         string
	PageSize         int
	MaxResponseBytes int64
	RequestAttempts  int
	RetryBackoff     time.Duration
}

type Client struct {
	httpClient HTTPDoer
	limiter    RateLimiter
	config     Config
}

type FetchPageRequest struct {
	Category string
	Start    int
}

type FetchPageResult struct {
	Records      []paper.Record
	Requests     int
	TotalResults int
}

type FetchError struct {
	Code      string
	Retryable bool
	Err       error
}

func (err *FetchError) Error() string { return err.Code + ": " + err.Err.Error() }
func (err *FetchError) Unwrap() error { return err.Err }

func NewClient(httpClient HTTPDoer, limiter RateLimiter, config Config) (*Client, error) {
	if httpClient == nil || limiter == nil || config.PageSize < 1 ||
		config.MaxResponseBytes < 1 || config.RequestAttempts < 1 || config.RetryBackoff < 0 {
		return nil, errors.New("invalid arxiv client configuration")
	}
	endpoint, err := url.Parse(config.Endpoint)
	if err != nil || endpoint.Scheme != "https" ||
		!strings.EqualFold(endpoint.Hostname(), "export.arxiv.org") {
		return nil, errors.New("invalid arxiv endpoint")
	}
	return &Client{httpClient: httpClient, limiter: limiter, config: config}, nil
}

func (client *Client) FetchPage(ctx context.Context, input FetchPageRequest) (FetchPageResult, error) {
	category := strings.TrimSpace(input.Category)
	if !categoryPattern.MatchString(category) || input.Start < 0 {
		return FetchPageResult{}, &FetchError{Code: "ARXIV_INVALID_REQUEST", Err: errors.New("invalid page request")}
	}

	feed, requests, err := client.fetchPage(ctx, category, input.Start)
	result := FetchPageResult{
		Records:  make([]paper.Record, 0, len(feed.Entries)),
		Requests: requests, TotalResults: feed.TotalResults,
	}
	if err != nil {
		return result, err
	}
	for _, entry := range feed.Entries {
		record, err := normalizeEntry(entry)
		if err != nil {
			return FetchPageResult{Requests: requests, TotalResults: feed.TotalResults},
				&FetchError{Code: "ARXIV_INVALID_ENTRY", Err: err}
		}
		result.Records = append(result.Records, record)
	}
	return result, nil
}

func (client *Client) fetchPage(
	ctx context.Context,
	category string,
	startIndex int,
) (atomFeed, int, error) {
	requestURL, err := client.buildURL(category, startIndex)
	if err != nil {
		return atomFeed{}, 0, &FetchError{Code: "ARXIV_BUILD_REQUEST", Err: err}
	}
	requests := 0
	for attempt := 1; attempt <= client.config.RequestAttempts; attempt++ {
		if err := client.limiter.Wait(ctx); err != nil {
			return atomFeed{}, requests, &FetchError{Code: "ARXIV_RATE_LIMITER", Retryable: true, Err: err}
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
		if err != nil {
			return atomFeed{}, requests, &FetchError{Code: "ARXIV_BUILD_REQUEST", Err: err}
		}
		request.Header.Set("User-Agent", userAgent)
		request.Header.Set("Accept", "application/atom+xml, application/xml")
		requests++
		response, err := client.httpClient.Do(request)
		if err != nil {
			if attempt < client.config.RequestAttempts {
				if err := waitForRetry(ctx, client.config.RetryBackoff*time.Duration(attempt)); err != nil {
					return atomFeed{}, requests, &FetchError{Code: "ARXIV_CANCELLED", Retryable: true, Err: err}
				}
				continue
			}
			return atomFeed{}, requests, &FetchError{Code: "ARXIV_NETWORK", Retryable: true, Err: err}
		}

		feed, retryable, responseErr := client.decodeResponse(response)
		if responseErr == nil {
			return feed, requests, nil
		}
		if retryable && attempt < client.config.RequestAttempts {
			if err := waitForRetry(ctx, client.config.RetryBackoff*time.Duration(attempt)); err != nil {
				return atomFeed{}, requests, &FetchError{Code: "ARXIV_CANCELLED", Retryable: true, Err: err}
			}
			continue
		}
		return atomFeed{}, requests, responseErr
	}
	return atomFeed{}, requests, &FetchError{Code: "ARXIV_RETRY_EXHAUSTED", Retryable: true, Err: errors.New("retry exhausted")}
}

func (client *Client) buildURL(
	category string,
	startIndex int,
) (string, error) {
	requestURL, err := url.Parse(client.config.Endpoint)
	if err != nil {
		return "", err
	}
	query := requestURL.Query()
	query.Set("search_query", "cat:"+category)
	query.Set("start", strconv.Itoa(startIndex))
	query.Set("max_results", strconv.Itoa(client.config.PageSize))
	query.Set("sortBy", "lastUpdatedDate")
	query.Set("sortOrder", "descending")
	requestURL.RawQuery = query.Encode()
	return requestURL.String(), nil
}

func (client *Client) decodeResponse(response *http.Response) (atomFeed, bool, error) {
	if response == nil {
		return atomFeed{}, true, &FetchError{Code: "ARXIV_NETWORK", Retryable: true, Err: errors.New("nil response")}
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		retryable := response.StatusCode == http.StatusTooManyRequests || response.StatusCode >= 500
		return atomFeed{}, retryable, &FetchError{
			Code: "ARXIV_HTTP_STATUS", Retryable: retryable,
			Err: fmt.Errorf("unexpected HTTP status %d", response.StatusCode),
		}
	}
	limited := io.LimitReader(response.Body, client.config.MaxResponseBytes+1)
	body, err := io.ReadAll(limited)
	if err != nil {
		return atomFeed{}, true, &FetchError{Code: "ARXIV_READ_RESPONSE", Retryable: true, Err: err}
	}
	if int64(len(body)) > client.config.MaxResponseBytes {
		return atomFeed{}, false, &FetchError{Code: "ARXIV_RESPONSE_TOO_LARGE", Err: errors.New("response exceeds configured limit")}
	}
	var feed atomFeed
	if err := xml.Unmarshal(body, &feed); err != nil {
		return atomFeed{}, false, &FetchError{Code: "ARXIV_INVALID_XML", Err: err}
	}
	return feed, false, nil
}

func normalizeEntry(entry atomEntry) (paper.Record, error) {
	externalWithVersion, err := externalIDFromURL(entry.ID)
	if err != nil {
		return paper.Record{}, err
	}
	externalID := externalWithVersion
	if match := versionPattern.FindStringSubmatch(externalWithVersion); len(match) == 3 {
		externalID = match[1]
	}
	publishedAt, err := time.Parse(time.RFC3339, strings.TrimSpace(entry.Published))
	if err != nil {
		return paper.Record{}, fmt.Errorf("parse published time: %w", err)
	}
	updatedAt, err := time.Parse(time.RFC3339, strings.TrimSpace(entry.Updated))
	if err != nil {
		return paper.Record{}, fmt.Errorf("parse updated time: %w", err)
	}
	authors := make([]string, 0, len(entry.Authors))
	for _, author := range entry.Authors {
		authors = append(authors, author.Name)
	}
	categories := make([]string, 0, len(entry.Categories))
	for _, category := range entry.Categories {
		categories = append(categories, category.Term)
	}
	primaryCategory := strings.TrimSpace(entry.PrimaryCategory.Term)
	if primaryCategory != "" && !contains(categories, primaryCategory) {
		categories = append([]string{primaryCategory}, categories...)
	}
	record := paper.Record{
		ArXivID: externalID,
		Title:   entry.Title, Abstract: entry.Summary, Authors: authors,
		Categories: categories, PublishedAt: publishedAt, ArXivUpdatedAt: updatedAt,
		ArXivURL: "https://arxiv.org/abs/" + externalID,
		PDFURL:   "https://arxiv.org/pdf/" + externalID,
	}
	return paper.NormalizeRecord(record)
}

func contains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func externalIDFromURL(raw string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") ||
		!strings.EqualFold(parsed.Hostname(), "arxiv.org") ||
		!strings.HasPrefix(parsed.EscapedPath(), "/abs/") {
		return "", errors.New("invalid arxiv entry id")
	}
	identifier := strings.TrimPrefix(parsed.EscapedPath(), "/abs/")
	identifier, err = url.PathUnescape(identifier)
	if err != nil || identifier == "" || strings.Contains(identifier, "..") {
		return "", errors.New("invalid arxiv entry id")
	}
	return identifier, nil
}

func waitForRetry(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return nil
	}
	jitter := time.Duration(rand.Int64N(int64(delay/4) + 1))
	timer := time.NewTimer(delay + jitter)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

type atomFeed struct {
	TotalResults int         `xml:"totalResults"`
	Entries      []atomEntry `xml:"entry"`
}

type atomEntry struct {
	ID              string         `xml:"id"`
	Updated         string         `xml:"updated"`
	Published       string         `xml:"published"`
	Title           string         `xml:"title"`
	Summary         string         `xml:"summary"`
	Authors         []atomAuthor   `xml:"author"`
	Categories      []atomCategory `xml:"category"`
	PrimaryCategory atomCategory   `xml:"primary_category"`
}

type atomAuthor struct {
	Name string `xml:"name"`
}

type atomCategory struct {
	Term string `xml:"term,attr"`
}
