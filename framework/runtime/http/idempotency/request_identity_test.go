package idempotency

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ghbvf/gocell/framework/kernel/clock/clockmock"
)

func TestRequestIdentityFromContext_AbsentByDefault(t *testing.T) {
	if _, ok := RequestIdentityFromContext(context.Background()); ok {
		t.Fatal("RequestIdentityFromContext should return ok=false on a bare context")
	}
}

// TestNewRequestIdentity_ValidatesKey: the sole constructor enforces full
// middleware-grade key validation, so a malformed key can never enter the command
// dedup path (#1610 F3 — sealed construction subsumes the bridge's old partial check).
func TestNewRequestIdentity_ValidatesKey(t *testing.T) {
	tests := []struct {
		name    string
		key     string
		wantErr bool
	}{
		{"valid", "abc-123", false},
		{"empty", "", true},
		{"too long", strings.Repeat("x", maxIdempotencyKeyLen+1), true},
		{"brace", "ab{c}", true},
		{"non-printable", "ab\x01c", true},
		{"non-ascii", "abcé", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewRequestIdentity("caller", "fp", tc.key)
			if got := err != nil; got != tc.wantErr {
				t.Fatalf("NewRequestIdentity(key=%q) err=%v, wantErr=%v", tc.key, err, tc.wantErr)
			}
		})
	}
}

// TestCommandDedupToken_FoldsGenuineDuplicates: same caller+fingerprint+key → same
// token (cross-cell/cross-pod duplicate folds to one slot, the #1610 goal).
func TestCommandDedupToken_FoldsGenuineDuplicates(t *testing.T) {
	a, _ := NewRequestIdentity("alice", "fp1", "k1")
	b, _ := NewRequestIdentity("alice", "fp1", "k1")
	if a.CommandDedupToken() != b.CommandDedupToken() {
		t.Fatal("identical (caller,fingerprint,key) must produce the same dedup token")
	}
}

// TestCommandDedupToken_SeparatesCallerAndPayload: #1610 F1 — different caller OR
// different payload (same key) must NOT fold into the same dedup slot.
func TestCommandDedupToken_SeparatesCallerAndPayload(t *testing.T) {
	base, _ := NewRequestIdentity("alice", "fp1", "k1")
	diffCaller, _ := NewRequestIdentity("bob", "fp1", "k1")
	diffPayload, _ := NewRequestIdentity("alice", "fp2", "k1")
	if base.CommandDedupToken() == diffCaller.CommandDedupToken() {
		t.Error("different caller + same key must NOT fold to the same dedup slot")
	}
	if base.CommandDedupToken() == diffPayload.CommandDedupToken() {
		t.Error("different payload + same key must NOT fold to the same dedup slot")
	}
}

// TestMiddleware_InjectsRequestIdentity: the middleware mints + injects the sealed
// identity into the downstream handler ctx (HTTP-side half of the #1610 bridge).
func TestMiddleware_InjectsRequestIdentity(t *testing.T) {
	clk := clockmock.New(time.Now())
	mw := Middleware(clk, NewMemStore(clk))
	var got RequestIdentity
	var ok bool
	down := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, ok = RequestIdentityFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	})
	mw(down).ServeHTTP(httptest.NewRecorder(),
		requestWithUserCtx("POST", "/", "idem-xyz", "tenant1", "user1"))

	if !ok {
		t.Fatal("downstream must see the minted RequestIdentity")
	}
	if got.CommandDedupToken() == "" {
		t.Fatal("identity dedup token must be non-empty")
	}
}

// TestMiddleware_NoKeyNoIdentity: without an Idempotency-Key the middleware passes
// through and the downstream sees no RequestIdentity.
func TestMiddleware_NoKeyNoIdentity(t *testing.T) {
	clk := clockmock.New(time.Now())
	mw := Middleware(clk, NewMemStore(clk))
	var ok bool
	down := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, ok = RequestIdentityFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	})
	mw(down).ServeHTTP(httptest.NewRecorder(),
		requestWithUserCtx("POST", "/", "", "tenant1", "user1"))

	if ok {
		t.Fatal("no Idempotency-Key → no RequestIdentity downstream")
	}
}
