package auditverify

import (
	"testing"

	"github.com/ghbvf/gocell/pkg/contracttest"
)

func TestEventAuditIntegrityVerifiedV1Publish(t *testing.T) {
	root := contracttest.ContractsRoot()
	_ = contracttest.LoadByID(t, root, "event.audit.integrity-verified.v1")
}
