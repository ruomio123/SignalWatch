package arxiv

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const feedResultLimit = 2000

type FeedConfig struct {
	Endpoint         string
	MaxResponseBytes int64
	RequestAttempts  int
	RetryBackoff     time.Duration
}

type FeedClient struct {
	httpClient HTTPDoer
	limiter    RateLimiter
	config     FeedConfig
}

type FeedResult struct {
	IDs      []string
	Requests int
}

func NewFeedClient(httpClient HTTPDoer, limiter RateLimiter, config FeedConfig) (*FeedClient, error) {
	if httpClient == nil || limiter == nil || config.MaxResponseBytes < 1 ||
		config.RequestAttempts < 1 || config.RetryBackoff < 0 {
		return nil, errors.New("invalid arxiv feed client configuration")
	}
	endpoint, err := url.Parse(config.Endpoint)
	if err != nil || endpoint.Scheme != "https" || !strings.EqualFold(endpoint.Hostname(), "rss.arxiv.org") {
		return nil, errors.New("invalid arxiv feed endpoint")
	}
	return &FeedClient{httpClient: httpClient, limiter: limiter, config: config}, nil
}

func (client *FeedClient) FetchIDs(ctx context.Context, category string) (FeedResult, error) {
	category = strings.TrimSpace(category)
	if !categoryPattern.MatchString(category) {
		return FeedResult{}, &FetchError{Code: "ARXIV_FEED_INVALID_REQUEST", Err: errors.New("invalid category")}
	}
	requestURL := strings.TrimRight(client.config.Endpoint, "/") + "/" + url.PathEscape(category)
	requests := 0
	for attempt := 1; attempt <= client.config.RequestAttempts; attempt++ {
		if err := client.limiter.Wait(ctx); err != nil {
			return FeedResult{Requests: requests}, &FetchError{Code: "ARXIV_RATE_LIMITER", Retryable: true, Err: err}
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
		if err != nil {
			return FeedResult{Requests: requests}, &FetchError{Code: "ARXIV_BUILD_REQUEST", Err: err}
		}
		request.Header.Set("User-Agent", userAgent)
		request.Header.Set("Accept", "application/atom+xml, application/xml")
		requests++
		response, err := client.httpClient.Do(request)
		if err == nil {
			ids, retryable, decodeErr := client.decode(response)
			if decodeErr == nil {
				return FeedResult{IDs: ids, Requests: requests}, nil
			}
			if !retryable || attempt == client.config.RequestAttempts {
				return FeedResult{Requests: requests}, decodeErr
			}
		} else if attempt == client.config.RequestAttempts {
			return FeedResult{Requests: requests}, &FetchError{Code: "ARXIV_NETWORK", Retryable: true, Err: err}
		}
		if err := waitForRetry(ctx, client.config.RetryBackoff*time.Duration(attempt)); err != nil {
			return FeedResult{Requests: requests}, &FetchError{Code: "ARXIV_CANCELLED", Retryable: true, Err: err}
		}
	}
	return FeedResult{Requests: requests}, &FetchError{Code: "ARXIV_RETRY_EXHAUSTED", Retryable: true, Err: errors.New("retry exhausted")}
}

func (client *FeedClient) decode(response *http.Response) ([]string, bool, error) {
	if response == nil {
		return nil, true, &FetchError{Code: "ARXIV_NETWORK", Retryable: true, Err: errors.New("nil response")}
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		retryable := response.StatusCode == http.StatusTooManyRequests || response.StatusCode >= 500
		return nil, retryable, &FetchError{Code: "ARXIV_HTTP_STATUS", Retryable: retryable, Err: fmt.Errorf("unexpected HTTP status %d", response.StatusCode)}
	}
	limited := io.LimitReader(response.Body, client.config.MaxResponseBytes+1)
	body, err := io.ReadAll(limited)
	if err != nil {
		return nil, true, &FetchError{Code: "ARXIV_READ_RESPONSE", Retryable: true, Err: err}
	}
	if int64(len(body)) > client.config.MaxResponseBytes {
		return nil, false, &FetchError{Code: "ARXIV_RESPONSE_TOO_LARGE", Err: errors.New("response exceeds configured limit")}
	}
	var feed struct {
		Entries []struct {
			ID string `xml:"id"`
		} `xml:"entry"`
	}
	if err := xml.Unmarshal(body, &feed); err != nil {
		return nil, false, &FetchError{Code: "ARXIV_INVALID_XML", Err: err}
	}
	if len(feed.Entries) >= feedResultLimit {
		return nil, false, &FetchError{Code: "ARXIV_FEED_LIMIT", Err: errors.New("feed result count reached the unpageable limit")}
	}
	ids := make([]string, 0, len(feed.Entries))
	for _, entry := range feed.Entries {
		identifier := strings.TrimSpace(entry.ID)
		const prefix = "oai:arXiv.org:"
		if !strings.HasPrefix(identifier, prefix) {
			return nil, false, &FetchError{Code: "ARXIV_INVALID_ENTRY", Err: errors.New("invalid feed entry id")}
		}
		identifier = strings.TrimPrefix(identifier, prefix)
		if !identifierPattern.MatchString(identifier) {
			return nil, false, &FetchError{Code: "ARXIV_INVALID_ENTRY", Err: errors.New("invalid feed arxiv id")}
		}
		ids = append(ids, identifier)
	}
	return ids, false, nil
}
