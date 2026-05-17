package testutil

import (
	"testing"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/runtime/auth/session"
	sessiontest "github.com/ghbvf/gocell/runtime/auth/session/sessiontest"
)

// RealSessionRepo returns a ready-to-use in-memory session.Store for unit
// tests. It wires the canonical test Protocol (sessiontest.Protocol — JTI-ref
// fingerprint, AuthzEpoch ordering, all CredentialEvents enabled) and a real
// clock. The returned store is safe for concurrent use (MemStore has an
// internal RWMutex).
//
// sessiontest.Protocol lives in runtime/auth/session/sessiontest/ so that
// SESSION-PROTOCOL-COMPOSITION-ROOT-01 archtest covers Protocol construction
// inside the allowlisted path; this helper itself does not call
// session.NewProtocol directly (session.MustNewProtocol was deleted in B2-K-02).
func RealSessionRepo(t testing.TB) *session.MemStore {
	t.Helper()
	store, err := session.NewMemStore(sessiontest.Protocol(), clock.Real())
	if err != nil {
		t.Fatalf("testutil.RealSessionRepo: store setup failed: %v", err)
	}
	return store
}
