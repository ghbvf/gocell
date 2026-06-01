// Package sagalog centralizes the per-instance saga log attribute set.
//
// It exists to make the invariant "every per-instance saga log carries
// lease_id" (#1266) a *structural* property rather than prose: instance_id and
// lease_id are REQUIRED POSITIONAL arguments of [InstanceFields], so a
// per-instance saga log cannot be emitted without lease_id — omitting it is a
// compile error (Hard downstream).
//
// The companion upstream guard is the archtest
// SAGA-SLOG-INSTANCE-FIELDS-CALLER-01 (tools/archtest/saga_invariants_test.go),
// which bans hand-written slog.String("instance_id"|"lease_id", …) anywhere in
// runtime/saga production code outside [InstanceFields] — forcing every
// per-instance log site through this carrier so any future site is covered
// automatically.
package sagalog

import (
	"log/slog"

	"github.com/ghbvf/gocell/pkg/idutil"
)

// InstanceFields returns the mandatory per-instance saga log attributes
// (instance_id, lease_id) followed by any caller-supplied extras such as
// definition_id, reason, error, step_name, or final_status.
//
// instanceID and leaseID are required positional arguments: a per-instance
// saga log cannot structurally omit lease_id. Use with (*slog.Logger).LogAttrs:
//
//	logger.LogAttrs(ctx, slog.LevelWarn, "saga: ...",
//	    sagalog.InstanceFields(inst.ID, leaseID,
//	        slog.String("definition_id", string(inst.DefinitionID)),
//	        slog.Any("error", err))...)
//
// The trailing `...` spreads the returned []slog.Attr as the variadic slog.Attr args LogAttrs expects.
func InstanceFields(instanceID, leaseID idutil.SafeID, extra ...slog.Attr) []slog.Attr {
	attrs := make([]slog.Attr, 0, 2+len(extra))
	attrs = append(attrs,
		slog.String("instance_id", string(instanceID)),
		slog.String("lease_id", string(leaseID)),
	)
	return append(attrs, extra...)
}
