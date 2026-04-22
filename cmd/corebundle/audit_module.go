package main

import (
	"context"

	auditcore "github.com/ghbvf/gocell/cells/auditcore"
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/runtime/bootstrap"
)

// AuditCoreModule wires auditcore: HMAC key + EventBus + cursor codec.
//
// ref: uber-go/fx fx.Module("auditcore", ...) — self-contained module.
// backlog: S29 CORE-BUNDLE-APP-BUILDER-01
type AuditCoreModule struct{}

// ID returns the stable identifier used in error messages.
func (AuditCoreModule) ID() string { return "auditcore" }

// Provide resolves all auditcore-specific dependencies and returns the
// constructed cell. Audit-core is in-memory only, so no bootstrap.Options
// are needed.
func (AuditCoreModule) Provide(_ context.Context, shared *SharedDeps) (cell.Cell, []bootstrap.Option, error) {
	c := auditcore.NewAuditCore(
		auditcore.WithInMemoryDefaults(),
		auditcore.WithPublisher(shared.EventBus),
		auditcore.WithHMACKey(shared.HMACKey),
		auditcore.WithCursorCodec(shared.CursorCodecs.audit),
	)
	return c, nil, nil
}

var _ CellModule = AuditCoreModule{}
