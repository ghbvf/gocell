package hello_test

// contract_test.go — contract test for http.demo.hello.v1.
//
// Test name TestHttpDemoHelloV1Serve is derived by the verify runner from
// slice.yaml verify.contract entry:
//
//	contract.http.demo.hello.v1.serve
//	→ fullPath = "http.demo.hello.v1.serve"
//	→ each segment camelized → "HttpDemoHelloV1Serve"
//	→ prefix "Test" → TestHttpDemoHelloV1Serve

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ghbvf/gocell/examples/demo/cells/democell/slices/hello"
	hellov1 "github.com/ghbvf/gocell/generated/contracts/http/demo/hello/v1"
	"github.com/ghbvf/gocell/tests/contracttest"
)

// TestHttpDemoHelloV1Serve verifies the hello contract: GET /api/v1/hello
// returns 200 with a schema-valid {"data":{"message":...}} body. The hello
// endpoint is pure compute with no error path, so there is no 4xx case to
// exercise (the contract's declared 400 exists only to satisfy the typed error
// envelope and is never emitted by the handler).
func TestHttpDemoHelloV1Serve(t *testing.T) {
	root := contracttest.ExampleContractsRoot(t, "demo")
	c := contracttest.LoadByID(t, root, "http.demo.hello.v1")

	svc, err := hello.NewService()
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	h := hellov1.NewHandler(hello.NewHandler(svc))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(c.HTTP.Method, c.HTTP.Path, nil)
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d; body: %s", rec.Code, rec.Body.String())
	}
	c.ValidateHTTPResponseRecorder(t, rec)
}
