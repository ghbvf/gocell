//go:build integration

package integration

import "testing"

// TestJAuditlogintrailEventConsume implements journeys/J-auditlogintrail.yaml
// passCriteria "session.created 事件被 auditcore 消费" — checkRef
// journey.J-auditlogintrail.event-consume. The verify runner resolves that
// ref to ^TestJAuditlogintrailEventConsume$ via verify.kebabToCamelCase.
//
// WAVE 1 RED placeholder — Wave 2 will replace this body with the actual
// docker-free assertion that drives auditcore.NewAuditCore + the
// auditappendsession subscription's HandleEvent, and asserts
// outbox.DispositionAck.
func TestJAuditlogintrailEventConsume(t *testing.T) {
	t.Fatal("RED: J-auditlogintrail.event-consume not implemented")
}
