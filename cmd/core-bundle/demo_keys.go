package main

import (
	"crypto/subtle"
	"fmt"
)

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
	"core-bundle-audit-cursor-key-32!",
	"core-bundle-cfg-cursor-key--32b!",
}

// rejectDemoKey returns an error if adapterMode == "real" and key matches a
// well-known demo value. Otherwise it is a no-op. Comparison uses
// constant-time equality even though these values are public constants, to
// keep the check semantically aligned with other secret comparisons.
func rejectDemoKey(adapterMode, envName string, key []byte) error {
	if adapterMode != "real" {
		return nil
	}
	for _, demo := range wellKnownDemoKeys {
		if len(key) == len(demo) && subtle.ConstantTimeCompare(key, []byte(demo)) == 1 {
			return fmt.Errorf("%s is set to a well-known demo key; rotate to a fresh random 32-byte secret before running in real adapter mode", envName)
		}
	}
	return nil
}
