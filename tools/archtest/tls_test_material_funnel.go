// Importable rule body for TLS-TEST-MATERIAL-FUNNEL-01. Non-test .go file so
// the dogfood + RED-fixture precision gate (tls_test_material_funnel_test.go)
// share a single scanner — no parallel rule body. The sanctioned set is matched
// by workspace-relative directory prefix (not import path), which is robust
// against go/packages test-variant path mangling (foo / foo_test / foo.test)
// and needs no module-path literal.
//
// # TLS-TEST-MATERIAL-FUNNEL-01
//
// crypto/x509.CreateCertificate — the act of minting an X.509 certificate — may
// only be called from a closed set of sanctioned packages:
//
//   - framework/runtime/http/tlsutil/tlsutiltest — the single sanctioned source
//     of self-signed mTLS cert material for tests (issue #2287). Before it, ~8
//     near-equivalent "self-signed CA + cell leaf" cert-gen helpers were copy-
//     pasted across the test tree (the mqtt copy even carried a "Ported from
//     adapters/grpc/cert_test.go" comment). cert-gen detail — curve, validity,
//     EKU, SAN — is sensitive to mTLS correctness, so a bug in one copy is
//     invisible to the others; centralizing the mint makes the shape a single
//     source and this funnel keeps it centralized.
//   - framework/runtime/certsigning, framework/runtime/certlifecycle — the
//     CSR→certificate issuance pipeline tests. They fabricate certificates as
//     system-under-test INPUT (the cert is what the signing/lifecycle code
//     consumes), not reusable cell-identity fixtures, so routing them through
//     tlsutiltest would be circular. Allowlisted with this rationale.
//   - adapters/softca — the production soft-CA implementation. It is the real
//     certificate authority (not test material); the scan covers production
//     packages too, so it is allowlisted here.
//
// Every other caller — production or test — is a violation.
//
// # AI-robust: Medium (downstream type-aware scan; no Hard ceiling)
//
// This is a Medium funnel, NOT Hard, and there is no low-cost Hard path:
// crypto/x509.CreateCertificate is a stdlib free function, so there is no
// sealed type or unexported field that could make an unsanctioned call
// unrepresentable at compile time (contrast CELLTLS-MATERIAL-FUNNEL-01, whose
// upstream is Hard because tlsutil.ClientIdentity is sealed). The downstream
// scan is type-aware: ResolvePackageRef resolves each callee to its owning
// package via go/types, so import aliases (x509alias.CreateCertificate) do not
// defeat it.
//
// Not registered in StandardCellRules: the sanctioned packages are GoCell-
// internal; an external Cell repo has no such files, so the allowlist would
// never match and the rule would degrade to a pure ban that false-reds any
// repo legitimately minting test certs from its own helper. Same disposition as
// CELLTLS-MATERIAL-FUNNEL-01.
//
// # Scope
//
// Scanned via Production(Tests:true) under both the default build context and
// FlatNonDefaultTags() (so integration / mqtt_tls-gated test files are covered),
// union-deduped. generated/ is excluded by Production; the archtest_fixture tag
// is deliberately absent from FlatNonDefaultTags (#944), so the RED fixture is
// never compiled into the dogfood.
//
// # Blind spots (BS)
//
//   - BS-1 Reverse build constraints (//go:build !tag): files excluded from a
//     -tags=...,tag,... union load are not scanned. No such cert-gen file
//     exists today; the nil-tags companion pass covers the default context.
//   - BS-2 CreateCertificateRequest (CSR creation) is intentionally NOT banned —
//     it is a distinct concern used legitimately by the pipeline tests and is
//     not cell mTLS cert material.
//   - BS-3 Function-value indirection (var f = x509.CreateCertificate; f(...))
//     resolves to *types.Var (ok=false) and is not flagged. Accepted: no
//     sanctioned or migrated caller does this.
//   - BS-4 Reflection construction: out of scope per ai-robust.md §3.
package archtest

import (
	"fmt"
	"go/ast"
	"strings"
	"testing"
)

const (
	tlsTestMaterialRuleID = "TLS-TEST-MATERIAL-FUNNEL-01"

	// tlsTestMaterialBannedPkg / tlsTestMaterialBannedCtor identify the minting
	// call this funnel governs: crypto/x509.CreateCertificate.
	tlsTestMaterialBannedPkg  = "crypto/x509"
	tlsTestMaterialBannedCtor = "CreateCertificate"
)

// tlsTestMaterialSanctionedDirs is the closed set of Pass.Rel directory prefixes
// whose files may call crypto/x509.CreateCertificate. See the package godoc for
// the per-entry rationale. Trailing slash makes each a true directory prefix (so
// "…/certsigning/" never matches "…/certsigningfoo/").
//
// Pass.Rel strips the core framework module's leading "framework/" segment (the
// #1565 split normalization in newPackageRel/stripFrameworkPrefix), so framework
// packages appear as "runtime/…" here, NOT "framework/runtime/…". Non-framework
// modules (adapters/, cellmodules/, tools/) are not stripped.
var tlsTestMaterialSanctionedDirs = []string{
	"runtime/http/tlsutil/tlsutiltest/",
	"runtime/certsigning/",
	"runtime/certlifecycle/",
	"adapters/softca/",
}

// CheckTLSTestMaterialFunnel enforces TLS-TEST-MATERIAL-FUNNEL-01 over the whole
// workspace. It scans the production tree with test variants under both the
// default build context and FlatNonDefaultTags() (integration / mqtt_tls etc.)
// and returns the union of diagnostics; GoCell's TestTLSTestMaterialFunnel calls
// it directly — single source, no parallel rule body.
func CheckTLSTestMaterialFunnel(t *testing.T) []Diagnostic {
	t.Helper()
	seen := map[string]struct{}{}
	var out []Diagnostic
	for _, tags := range [][]string{nil, FlatNonDefaultTags()} {
		for _, d := range Run(t, Production(TypedOpts{Tests: true, Tags: tags}), scanTLSTestMaterialViolations) {
			key := fmt.Sprintf("%s:%d", d.Rel, d.Line)
			if _, dup := seen[key]; dup {
				continue
			}
			seen[key] = struct{}{}
			out = append(out, d)
		}
	}
	return out
}

// scanTLSTestMaterialViolations walks every CallExpr in pass.Files, resolves the
// callee to its (pkgPath, name) tuple via ResolvePackageRef, and flags calls to
// crypto/x509.CreateCertificate whose file is not under a sanctioned directory.
func scanTLSTestMaterialViolations(p *Pass) []Diagnostic {
	var out []Diagnostic
	for _, file := range p.Files {
		rel := p.Rel(file)
		if isTLSTestMaterialSanctioned(rel) {
			continue
		}
		EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
			if d, ok := tlsTestMaterialCallViolation(p, rel, call); ok {
				out = append(out, d)
			}
		})
	}
	return out
}

// tlsTestMaterialCallViolation returns the diagnostic for a single CallExpr if it
// is an unsanctioned call to crypto/x509.CreateCertificate — ok=false otherwise.
func tlsTestMaterialCallViolation(p *Pass, rel string, call *ast.CallExpr) (Diagnostic, bool) {
	calleePkg, name, ok := ResolvePackageRef(p.TypesInfo, call.Fun)
	if !ok || calleePkg != tlsTestMaterialBannedPkg || name != tlsTestMaterialBannedCtor {
		return Diagnostic{}, false
	}
	return Diagnostic{
		Rel:  rel,
		Line: p.Fset.Position(call.Pos()).Line,
		Message: fmt.Sprintf(
			"crypto/x509.CreateCertificate mints an X.509 certificate; "+
				"callers are restricted to the sanctioned packages "+
				"(tlsutiltest for test mTLS material, certsigning/certlifecycle "+
				"pipeline tests, adapters/softca production CA) (%s). "+
				"For test mTLS cert material use "+
				"framework/runtime/http/tlsutil/tlsutiltest",
			tlsTestMaterialRuleID,
		),
	}, true
}

// isTLSTestMaterialSanctioned reports whether the workspace-relative file path
// rel is under one of the sanctioned directories. Pure function for unit
// testability (TestIsTLSTestMaterialSanctioned).
func isTLSTestMaterialSanctioned(rel string) bool {
	for _, dir := range tlsTestMaterialSanctionedDirs {
		if strings.HasPrefix(rel, dir) {
			return true
		}
	}
	return false
}
