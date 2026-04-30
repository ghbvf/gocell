package main

import (
	"crypto/subtle"
	"fmt"
)

// realAdapterMode is the canonical value of GOCELL_ADAPTER_MODE that activates
// production fail-fast behavior (rejects demo keys, requires service secrets,
// rejects static Vault tokens, …). Defined as a constant so that future tokens
// like "staging" would surface as an unknown-mode compile site rather than
// silently being treated as "not real".
const realAdapterMode = "real"

// isRealMode reports whether the given adapter mode activates real-mode guards.
// Prefer this helper over inline `adapterMode == "real"` so the set of accepted
// real-mode values stays in one place.
func isRealMode(adapterMode string) bool {
	return adapterMode == realAdapterMode
}

// DO NOT COPY TO PRODUCTION: every entry below is published in this source
// tree (and in git history) — anyone can sign with these values. They exist
// solely so real-mode startup can detect and refuse them. Generate fresh
// 32-byte secrets via `openssl rand -hex 32` (or your secrets manager) for
// any non-demo deployment.
//
// wellKnownDemoKeys lists key material that shipped as public dev defaults at
// various points in GoCell's history. Real-mode startup must refuse to run
// with any of these values to prevent accidental production deployments with
// public keys. The list is append-only: once a key is retired here, never
// remove the entry — that would silently re-enable it.
//
// ref: zeromicro/go-zero core/service/serviceconf.go — strict mode rejects
// insecure defaults at SetUp() time.
// ref: kubernetes/kubernetes — kube-apiserver refuses to start when signing
// material is missing / insecure.
var wellKnownDemoKeys = []string{
	// HMAC (audit hash chain)
	"dev-hmac-key-replace-in-prod!!!!",

	// Cursor HMAC secrets (historical per-cell demo keys)
	"gocell-demo-AUDIT--CORE-key-32!!",
	"gocell-demo-CONFIG-CORE-key-32!!",
	"gocell-demo-ORDER-CELL-key-32b!!",
	"gocell-demo-DEVICE-CELL-key-32!!",
	"gocell-demo-ACCESS-CORE-key-32!!",
	"corebundle-audit-cursor-key-32b!",
	"corebundle-cfg-cursor-key--32bb!",
	"corebundle-access-cursor-key32!!",

	// Service token HMAC (shipped as test fixture; never use in production)
	"service-secret-32-bytes-xxxxxx!!",

	// AES master key (hex-encoded, 64 chars) shipped as test fixture in
	// cmd/corebundle and CI; real mode must refuse this value.
	"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
}

// rejectDemoKey returns an error if adapterMode == "real" and key matches a
// well-known demo value. Otherwise it is a no-op. Comparison uses
// constant-time equality even though these values are public constants, to
// keep the check semantically aligned with other secret comparisons.
func rejectDemoKey(adapterMode, envName string, key []byte) error {
	if !isRealMode(adapterMode) {
		return nil
	}
	for _, demo := range wellKnownDemoKeys {
		if len(key) == len(demo) && subtle.ConstantTimeCompare(key, []byte(demo)) == 1 {
			return fmt.Errorf("%s is set to a well-known demo key; rotate to a fresh random 32-byte secret before running in real adapter mode", envName)
		}
	}
	return nil
}
