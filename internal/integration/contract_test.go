package integration_test

import (
	"bytes"
	"context"
	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers"
	"github.com/getkin/kin-openapi/routers/legacy"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
)

var contractOnce sync.Once
var contractRouter routers.Router
var contractErr error

// Every in-process API integration request validates the actual handler output.
func validateWireContract(t *testing.T, request *http.Request, body []byte, response *httptest.ResponseRecorder) {
	t.Helper()
	contractOnce.Do(func() {
		_, file, _, _ := runtime.Caller(0)
		doc, err := openapi3.NewLoader().LoadFromFile(filepath.Join(filepath.Dir(file), "../../api/openapi.yaml"))
		if err != nil {
			contractErr = err
			return
		}
		contractRouter, contractErr = legacy.NewRouter(doc)
	})
	if contractErr != nil {
		t.Fatal(contractErr)
	}
	route, params, err := contractRouter.FindRoute(request)
	if err != nil {
		t.Fatalf("undocumented route: %v", err)
	}
	options := &openapi3filter.Options{AuthenticationFunc: openapi3filter.NoopAuthenticationFunc, IncludeResponseStatus: true}
	request.Body = io.NopCloser(bytes.NewReader(body))
	input := &openapi3filter.RequestValidationInput{Request: request, PathParams: params, Route: route, Options: options}
	if response.Code < 400 {
		if err := openapi3filter.ValidateRequest(context.Background(), input); err != nil {
			t.Fatalf("request contract %s %s: %v", request.Method, request.URL.Path, err)
		}
	}
	output := &openapi3filter.ResponseValidationInput{RequestValidationInput: input, Status: response.Code, Header: response.Header(), Body: io.NopCloser(bytes.NewReader(response.Body.Bytes())), Options: options}
	if err := openapi3filter.ValidateResponse(context.Background(), output); err != nil {
		t.Fatalf("response contract %s %s: %v", request.Method, request.URL.Path, err)
	}
}
