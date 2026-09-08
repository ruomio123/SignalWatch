package paper

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"signalwatch/internal/platform/httpx"
)

type queryServiceStub struct {
	page    QueryPage
	paper   PublicPaper
	listErr error
	getErr  error
	userID  uint64
	input   QueryInput
	paperID uint64
}

func (stub *queryServiceStub) List(
	_ context.Context, userID uint64, input QueryInput,
) (QueryPage, error) {
	stub.userID, stub.input = userID, input
	return stub.page, stub.listErr
}

func (stub *queryServiceStub) Get(
	_ context.Context, userID, paperID uint64,
) (PublicPaper, error) {
	stub.userID, stub.paperID = userID, paperID
	return stub.paper, stub.getErr
}

func newPaperQueryRouter(stub PaperQueryService, authenticated bool) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(httpx.RequestIDMiddleware())
	if authenticated {
		router.Use(func(c *gin.Context) {
			httpx.SetCurrentUserID(c, 42)
			c.Next()
		})
	}
	handler := NewQueryHandler(stub, slog.New(slog.NewTextHandler(io.Discard, nil)))
	router.GET("/papers", handler.List)
	router.GET("/papers/:id", handler.Get)
	return router
}

func TestQueryHandlerParsesListAndReturnsPage(t *testing.T) {
	stub := &queryServiceStub{page: QueryPage{Items: []PublicPaper{}, Page: 2, PageSize: 5, Total: 0}}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/papers?page=2&page_size=5&subscription_id=9", nil)
	newPaperQueryRouter(stub, true).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || stub.userID != 42 || stub.input.Page != 2 ||
		stub.input.PageSize != 5 || stub.input.Filter.SubscriptionID == nil ||
		*stub.input.Filter.SubscriptionID != 9 {
		t.Fatalf("unexpected response=%d stub=%+v", recorder.Code, stub)
	}
}

func TestQueryHandlerRejectsInvalidQueriesAndIDs(t *testing.T) {
	for _, path := range []string{
		"/papers?page=0", "/papers?page_size=101", "/papers?subscription_id=0", "/papers/not-an-id",
	} {
		recorder := httptest.NewRecorder()
		newPaperQueryRouter(&queryServiceStub{}, true).
			ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("%s: expected 400, got %d", path, recorder.Code)
		}
		var response httpx.ErrorResponse
		if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil ||
			response.Code != httpx.CodeValidationError {
			t.Fatalf("%s: unexpected error response %+v decode=%v", path, response, err)
		}
	}
}

func TestQueryHandlerProtectsOwnershipAndMapsNotFound(t *testing.T) {
	unauthorized := httptest.NewRecorder()
	newPaperQueryRouter(&queryServiceStub{}, false).
		ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, "/papers", nil))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("expected unauthorized, got %d", unauthorized.Code)
	}

	notFound := httptest.NewRecorder()
	newPaperQueryRouter(&queryServiceStub{getErr: ErrQueryNotFound}, true).
		ServeHTTP(notFound, httptest.NewRequest(http.MethodGet, "/papers/99", nil))
	if notFound.Code != http.StatusNotFound {
		t.Fatalf("expected not found, got %d", notFound.Code)
	}
	var response httpx.ErrorResponse
	if err := json.Unmarshal(notFound.Body.Bytes(), &response); err != nil || response.Code != CodePaperNotFound {
		t.Fatalf("unexpected not-found response %+v decode=%v", response, err)
	}
}
