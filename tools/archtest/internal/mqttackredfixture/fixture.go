//go:build archtest_fixture

// Package mqttackredfixture is the real-source red fixture for
// MQTT-ACK-CALLSITE-FUNNEL-01/K2's non-vacuous proof (#1287). It plants a REAL
// (*github.com/eclipse/paho.golang/paho.Client).Ack callsite so the K2 typed
// detector (ResolveMethodCall → FullName == pahoClientAckFullName) is proven
// non-vacuous against the ACTUAL paho type — the same go/types resolution path
// K2 runs over production code. This replaces the prior synthetic in-memory
// snippet (which only exercised the *ast.SelectorExpr.Sel-name precursor).
//
// Unlike internal/mqttredfixture (which uses a shape REPLICA because
// mqtt.ClientID.value is unexported and cannot be literal-constructed
// cross-package), paho.Client is an EXTERNAL exported type, so this fixture can
// reference *paho.Client and call .Ack directly — which is exactly the form K2
// bans in adapters/mqtt production code. The file compiles but is gated behind
// the archtest_fixture build tag (injected only by the Run dispatch for a
// fixtureRunScope), so it never appears in a normal build.
//
// ref: tools/archtest/mqtt_callsite_funnel_test.go TestMQTTAckCallsiteFunnel_K2_ScannerNonVacuous
// ref: tools/archtest/fixture.go FixtureOpts
// ref: gh #1287
package mqttackredfixture

import "github.com/eclipse/paho.golang/paho"

// archtestRedDirectPahoAck intentionally plants the K2-forbidden form: a direct
// (*paho.Client).Ack callsite. The K2 typed detector MUST resolve and report it.
//
//nolint:unused // referenced exclusively by the archtest scanner via AST loading.
func archtestRedDirectPahoAck(c *paho.Client, pb *paho.Publish) { _ = c.Ack(pb) }
