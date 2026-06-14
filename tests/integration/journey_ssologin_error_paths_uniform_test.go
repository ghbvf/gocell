//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/pkg/httputil"
)

// TestJSsologinErrorPathsUniform implements journeys/J-ssologin.yaml
// passCriteria "登录失败三态归一" — checkRef
// journey.J-ssologin.error-paths-uniform. The verify runner resolves that
// ref to ^TestJSsologinErrorPathsUniform$ via verify.kebabToCamelCase and
// executes it under -tags=integration. Because J-ssologin is
// lifecycle: active, governance VERIFY-06 (kernel/governance/rules_verify.go
// function validateVERIFY06Journey) runs this test inside
// `gocell validate --strict` (part of the Docker-less `make verify` gate) —
// so this test MUST be Docker-free, mirroring the layer-compromise rationale
// of TestJSsologinSessionDb in this package.
//
// The journey criterion is the FU-1 (#513) anti-enumeration contract
// (contracts/http/auth/login/v1/contract.yaml:24): missing-user /
// wrong-password / inactive-account (including locked and suspended) all
// collapse to ONE opaque HTTP 401 envelope so an attacker cannot distinguish
// "user does not exist" from "wrong password" from "account locked".
//
// sessionlogin constructs FIVE errcode values for login failure across its
// two-phase (pre-bcrypt + loginInTx) code paths. All five share
// KindUnauthenticated + ErrAuthLoginFailed + "invalid credentials"; only
// the WithInternal diagnostic (server-side slog only, never on wire) differs:
//
//   - service.go:262 — user lookup fail (pre-bcrypt);
//     WithInternal "user lookup failed: ..."
//   - service.go:275 — pre-bcrypt baseline fail (inactive/locked/suspended);
//     WithInternal "credentialauthority: pre-bcrypt baseline fail ..."
//   - service.go:280 — wrong password; no WithInternal
//   - service.go:352 — in-tx credentialauthority assert failed (concurrent
//     deactivation race); WithInternal "credentialauthority: in-tx assert failed ..."
//   - service.go:484 — in-tx FOR UPDATE user row vanished (concurrent delete
//     race); no WithInternal (KindNotFound → classifyForUpdateErr)
//
// These five points represent 3 client-observable states:
// missing-user (including its in-tx race variant at :484) /
// wrong-password / inactive (including locked·suspended and their in-tx
// race variant at :352). This test reconstructs all five errcode shapes
// and asserts their CLIENT-FACING wire bytes are byte-identical after the
// canonical framework error writer (pkg/httputil.WriteError, the single
// source of truth for the v1 error envelope). Byte-identity is the
// load-bearing assertion: it proves the WithInternal divergence (including
// the presence/absence of WithInternal across the five points) does not leak
// to the wire and all five paths are indistinguishable to a client.
//
// Layer compromise (same philosophy as TestJSsologinSessionDb): the wire
// envelope is governed solely by pkg/errcode + pkg/httputil, so asserting at
// that seam IS asserting the wire-shape contract. The full programmatic
// proof that sessionlogin's SERVICE paths actually return this errcode over a
// real HTTP + PG roundtrip is owned by T4's
// tests/integration/l2atomicity/TestL2_LoginUniform401 (testcontainers e2e)
// — T4 is the PRIMARY defense carrier for the anti-enumeration ✅ in §3
// threat matrix (real HTTP+PG roundtrip verifying service routing correctness);
// FU-4 (this test) is a DECLARATIVE spec supplement covering the
// errcode→wire serialization layer and is NOT a substitute for T4. See
// tests/integration/l2atomicity/doc.go:55-57 and plan
// docs/plans/202605082145-034-pg-corecell-b-route-plan.md §T4 ↔ FU-4 for
// the explicit T4 (programmatic) ↔ FU-4 (declarative journey spec)
// division of labor. Routing this test through PG/testcontainers would
// both duplicate T4 and fail VERIFY-06's Docker-less execution gate.
//
// errMsgInvalidCredentials ("invalid credentials") is unexported in the
// sessionlogin package; the literal here is the wire contract value pinned
// in contracts/http/auth/login/v1/contract.yaml:24.
func TestJSsologinErrorPathsUniform(t *testing.T) {
	t.Parallel()

	const wantMessage = "invalid credentials"

	// The five login-failure construction points, mirroring service.go:262 /
	// :275 / :280 / :352 / :484. Only WithInternal differs across the five
	// points; it must never reach the wire.
	//
	// NOTE on WithInternal literals: WithInternal content does NOT appear on
	// the wire (the byte-identity assertion below is specifically founded on
	// this). The string literals here ("u-123", "locked", "bob", etc.) are
	// stand-in values used to construct "WithInternal present with some text" /
	// "WithInternal absent" variants to prove that neither the presence nor the
	// content of WithInternal leaks to the client. They are NOT server-side slog
	// warning pattern templates and must not be used as such by ops tooling.
	paths := []struct {
		name string
		err  *errcode.Error
	}{
		{
			name: "missing-user",
			err: errcode.New(errcode.KindUnauthenticated, errcode.ErrAuthLoginFailed,
				wantMessage,
				errcode.WithInternal(errcode.InternalAttr("_", "user lookup failed: sql: no rows in result set"))),
		},
		{
			name: "inactive-account",
			err: errcode.New(errcode.KindUnauthenticated, errcode.ErrAuthLoginFailed,
				wantMessage,
				errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf(
					"credentialauthority: pre-bcrypt baseline fail (user_id=%s status=%v)",
					"u-123", "locked")))),
		},
		{
			name: "wrong-password",
			err: errcode.New(errcode.KindUnauthenticated, errcode.ErrAuthLoginFailed,
				wantMessage),
		},
		{
			// Mirrors service.go:352 — in-tx credentialauthority.Assert failed after
			// FOR UPDATE re-fetch (concurrent deactivation race). WithInternal carries
			// username + the assert error; content stays server-side only.
			name: "in-tx-inactive-race",
			err: errcode.New(errcode.KindUnauthenticated, errcode.ErrAuthLoginFailed,
				wantMessage,
				errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf(
					"credentialauthority: in-tx assert failed (user=%s): %v",
					"bob", "account is locked")))),
		},
		{
			// Mirrors service.go:484 — classifyForUpdateErr maps KindNotFound (user
			// row vanished between pre-bcrypt read and FOR UPDATE re-fetch) to the
			// uniform 401. No WithInternal (classifyForUpdateErr constructs bare).
			name: "in-tx-missinguser-race",
			err: errcode.New(errcode.KindUnauthenticated, errcode.ErrAuthLoginFailed,
				wantMessage),
		},
	}

	var canonicalBody []byte
	for i, p := range paths {
		rec := httptest.NewRecorder()
		httputil.WriteError(context.Background(), rec, p.err)

		assert.Equal(t, http.StatusUnauthorized, rec.Code,
			"%s: must collapse to HTTP 401", p.name)
		assert.Equal(t, "application/json", rec.Header().Get("Content-Type"),
			"%s: canonical error envelope is JSON", p.name)

		body := rec.Body.Bytes()

		// Structural assertion: the FU-1 wire contract values.
		var env struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
				Details []struct {
					Key   string `json:"key"`
					Value any    `json:"value"`
				} `json:"details"`
			} `json:"error"`
		}
		require.NoError(t, json.Unmarshal(body, &env), "%s: body must be the canonical envelope", p.name)
		assert.Equal(t, "ERR_AUTH_LOGIN_FAILED", env.Error.Code, "%s: code", p.name)
		assert.Equal(t, wantMessage, env.Error.Message, "%s: message", p.name)
		assert.Empty(t, env.Error.Details,
			"%s: no client-visible details (WithInternal must not leak)", p.name)

		// Anti-enumeration assertion: every path produces byte-identical wire
		// output. The WithInternal divergence (including wrong-password having
		// none) must not be observable by a client.
		if i == 0 {
			canonicalBody = append([]byte(nil), body...)
			continue
		}
		assert.Equal(t, string(canonicalBody), string(body),
			"%s: wire body must be byte-identical to missing-user (anti-enumeration)", p.name)
	}
}
