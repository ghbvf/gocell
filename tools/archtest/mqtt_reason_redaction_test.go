//go:build archtest

// INVARIANT: REASON-NAME-REDACTION-01
//
// adapters/mqtt builds error details carrying an MQTT reason code + its spec
// name for CONNACK / SUBACK / PUBACK rejections. For auth-related reason codes
// (e.g. 0x87 Not Authorized) the human-readable reasonName MUST be routed to the
// Internal channel (server logs only), not Public details, so it cannot help an
// attacker enumerate "credentials wrong vs authz missing". That auth-redaction
// decision lives in a single funnel — reasonDetailOptions (adapters/mqtt/errors.go).
//
// This invariant bans errcode.PublicString("reasonName", …) anywhere in
// adapters/mqtt production code outside reasonDetailOptions, so no per-packet
// error builder can re-introduce (or forget) the redaction — the bug F11 fixed
// (SUBACK + PUBACK leaked reasonName in Public details while CONNACK redacted it).
//
// AI-robust grading: Medium. The funnel is a typed-function callsite allowlist
// resolved with go/types (callee identity via ResolvePackageRef + const-arg
// evaluation via EvaluateConstString), NOT a string anchor. It is not Hard
// upstream: errcode.PublicString is a public constructor callable anywhere, and
// a determined edit could build the detail via a non-constant key the const-scan
// cannot see. The callee-resolved + const-key allowlist is the Go-reachable
// ceiling for this shape; a Hard form would require a sealed reasonName-detail
// type, tracked as future work if the leak class recurs.
//
// Scanner logic lives in mqtt_reason_redaction.go (importable non-test file).

package archtest

import (
	"go/ast"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestMQTTReasonNameRedaction_FunnelOnly enforces REASON-NAME-REDACTION-01:
// every errcode.PublicString("reasonName", …) in adapters/mqtt production code
// is enclosed by reasonDetailOptions.
func TestMQTTReasonNameRedaction_FunnelOnly(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}
	Report(t, "REASON-NAME-REDACTION-01",
		CheckMQTTReasonNameRedaction(t, ConfigForExternalCell{BuildTags: FlatNonDefaultTags()}))
}

// TestMQTTReasonNameRedaction_ScannerNonVacuous proves the detector fires on the
// real funnel callsite (so the FunnelOnly test cannot pass vacuously if the
// go/types resolution path silently breaks).
func TestMQTTReasonNameRedaction_ScannerNonVacuous(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}
	var inside int
	_ = Run(t, Typed(TypedOpts{Tests: false, Tags: FlatNonDefaultTags()},
		[]string{mqttPkgPath}),
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil || p.TypesInfo == nil || p.Pkg.Path() != mqttPkgPath {
				return nil
			}
			for _, f := range p.Files {
				if strings.HasSuffix(p.Rel(f), "_test.go") {
					continue
				}
				EachInSubtree[ast.CallExpr](f, func(call *ast.CallExpr) {
					if mqttIsReasonNamePublicStringCall(p, call) &&
						mqttEnclosingFuncName(f, call.Pos()) == mqttReasonDetailFunnelFunc {
						inside++
					}
				})
			}
			return nil
		})

	assert.GreaterOrEqual(t, inside, 1,
		"REASON-NAME-REDACTION-01: scanner found 0 PublicString(\"reasonName\") inside reasonDetailOptions — "+
			"the go/types resolution path may be silently broken")
}
