package arxiv

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestFeedClientBuildsCategoryURLAndParsesVersionedIDs(t *testing.T) {
	body := `<?xml version="1.0"?><feed xmlns="http://www.w3.org/2005/Atom">
<entry><id>oai:arXiv.org:2609.00001v1</id></entry>
<entry><id>oai:arXiv.org:hep-th/9901001v3</id></entry></feed>`
	var requested *http.Request
	client, err := NewFeedClient(doerFunc(func(request *http.Request) (*http.Response, error) {
		requested = request
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))}, nil
	}), limiterFunc(func(context.Context) error { return nil }), FeedConfig{
		Endpoint: "https://rss.arxiv.org/atom", MaxResponseBytes: 1 << 20, RequestAttempts: 1,
	})
	if err != nil {
		t.Fatalf("new Feed client: %v", err)
	}
	result, err := client.FetchIDs(context.Background(), "cs.AI")
	if err != nil {
		t.Fatalf("fetch Feed: %v", err)
	}
	if requested.URL.String() != "https://rss.arxiv.org/atom/cs.AI" ||
		len(result.IDs) != 2 || result.IDs[0] != "2609.00001v1" || result.IDs[1] != "hep-th/9901001v3" {
		t.Fatalf("unexpected Feed request/result: url=%s result=%+v", requested.URL, result)
	}
}

func TestFeedClientRejectsMalformedAndUnconfirmablyFullFeeds(t *testing.T) {
	full := strings.Builder{}
	full.WriteString(`<feed xmlns="http://www.w3.org/2005/Atom">`)
	for index := range feedResultLimit {
		full.WriteString(fmt.Sprintf(`<entry><id>oai:arXiv.org:2609.%05dv1</id></entry>`, index))
	}
	full.WriteString(`</feed>`)
	for _, test := range []struct {
		name     string
		body     string
		maxBytes int64
		code     string
	}{
		{name: "malformed id", body: `<feed xmlns="http://www.w3.org/2005/Atom"><entry><id>bad</id></entry></feed>`, maxBytes: 1 << 20, code: "ARXIV_INVALID_ENTRY"},
		{name: "invalid XML", body: `<feed><entry>`, maxBytes: 1 << 20, code: "ARXIV_INVALID_XML"},
		{name: "oversized", body: strings.Repeat("x", 101), maxBytes: 100, code: "ARXIV_RESPONSE_TOO_LARGE"},
		{name: "feed limit", body: full.String(), maxBytes: 1 << 20, code: "ARXIV_FEED_LIMIT"},
	} {
		t.Run(test.name, func(t *testing.T) {
			client, err := NewFeedClient(doerFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(test.body))}, nil
			}), limiterFunc(func(context.Context) error { return nil }), FeedConfig{
				Endpoint: "https://rss.arxiv.org/atom", MaxResponseBytes: test.maxBytes, RequestAttempts: 1,
			})
			if err != nil {
				t.Fatalf("new Feed client: %v", err)
			}
			_, err = client.FetchIDs(context.Background(), "cs.AI")
			var fetchError *FetchError
			if !errors.As(err, &fetchError) || fetchError.Code != test.code {
				t.Fatalf("expected %s, got %T %v", test.code, err, err)
			}
		})
	}
}
