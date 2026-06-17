package registrywrite

import (
	"context"
	"testing"
	"time"

	"github.com/ghbvf/gocell/framework/kernel/clock/clockmock"
	"github.com/ghbvf/gocell/framework/kernel/registry"
	"github.com/ghbvf/gocell/framework/runtime/auth"
	submit "github.com/ghbvf/gocell/generated/contracts/http/registry/contract/submit/v1"
)

func mustTime(s string) time.Time {
	ts, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic("mustTime: " + err.Error())
	}
	return ts
}

func newService(t *testing.T) (*Service, *registry.ContractRegistrar) {
	t.Helper()
	registrar := registry.NewContractRegistrar(clockmock.New(testEpoch))
	svc, err := NewService(clockmock.New(testEpoch), registrar)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	return svc, registrar
}

func principalCtx(subject string) context.Context {
	return auth.WithPrincipal(context.Background(), &auth.Principal{
		Kind: auth.PrincipalUser, Subject: subject, AuthMethod: "test",
	})
}

// TestNewService_NilRegistrar pins the required-dep fail-fast.
func TestNewService_NilRegistrar(t *testing.T) {
	if _, err := NewService(clockmock.New(testEpoch), nil); err == nil {
		t.Fatal("NewService(nil registrar) must error (gocell:\"required\")")
	}
}

// TestSubmit_Success: a valid submit records a submitted registration whose wire
// state derives from the sealed RegistrationState and whose submitter is the
// authenticated principal subject.
func TestSubmit_Success(t *testing.T) {
	svc, _ := newService(t)
	resp, err := svc.Submit(principalCtx("cell-a"), &submit.Request{ID: "http.example.foo.v1", Kind: submit.RequestKindHTTP, PayloadSchema: "sha256:abc"})
	if err != nil {
		t.Fatalf("Submit: unexpected error %v", err)
	}
	ok, isOK := resp.(submit.Submit201JSONResponse)
	if !isOK {
		t.Fatalf("Submit returned %T, want Submit201JSONResponse", resp)
	}
	if ok.Data == nil {
		t.Fatal("Submit201JSONResponse.Data is nil")
	}
	if ok.Data.ID != "http.example.foo.v1" || ok.Data.Kind != "http" {
		t.Errorf("Data id/kind = %q/%q, want http.example.foo.v1/http", ok.Data.ID, ok.Data.Kind)
	}
	if ok.Data.State != registry.StateSubmitted().String() {
		t.Errorf("Data.State = %q, want %q (sealed)", ok.Data.State, registry.StateSubmitted().String())
	}
	if ok.Data.Submitter != "cell-a" {
		t.Errorf("Data.Submitter = %q, want cell-a (from principal)", ok.Data.Submitter)
	}
	if ok.Data.CreatedAt == "" || ok.Data.UpdatedAt == "" {
		t.Error("Data timestamps must be set")
	}
}

// TestSubmit_Duplicate: a second submit of the same id is a real 409 from the
// registrar dedup, returned as the typed Submit409ErrorResponse.
func TestSubmit_Duplicate(t *testing.T) {
	svc, _ := newService(t)
	ctx := principalCtx("cell-a")
	req := &submit.Request{ID: "dup.v1", Kind: submit.RequestKindEvent}
	if _, err := svc.Submit(ctx, req); err != nil {
		t.Fatalf("first Submit: %v", err)
	}
	resp, err := svc.Submit(ctx, req)
	if err != nil {
		t.Fatalf("duplicate Submit: unexpected Go error %v (want typed 409)", err)
	}
	if _, ok := resp.(submit.Submit409ErrorResponse); !ok {
		t.Fatalf("duplicate Submit returned %T, want Submit409ErrorResponse", resp)
	}
}

// TestSubmit_MissingSubmitter: with no principal in context (a defensive path —
// the gate guarantees one in production) the registrar rejects the empty
// submitter and the service returns the typed 400, never a panic.
func TestSubmit_MissingSubmitter(t *testing.T) {
	svc, _ := newService(t)
	resp, err := svc.Submit(context.Background(), &submit.Request{ID: "x.v1", Kind: submit.RequestKindHTTP})
	if err != nil {
		t.Fatalf("Submit: unexpected Go error %v (want typed 400)", err)
	}
	if _, ok := resp.(submit.Submit400ErrorResponse); !ok {
		t.Fatalf("Submit with no principal returned %T, want Submit400ErrorResponse", resp)
	}
}
