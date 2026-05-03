// cell_routes.go retains event spec declarations used by Init() in cell_init.go.
// HTTP route groups and event subscription wiring were folded into Init() as
// part of the Batch 3 Registry migration.
package accesscore

import "github.com/ghbvf/gocell/kernel/wrapper"

// errFmtSubscribe is the wrap format used by Init when a Subscribe call
// rejects a spec. Centralized so the four wrap sites stay aligned and
// SonarCloud's duplicate-literal rule is satisfied.
const errFmtSubscribe = "accesscore: subscribe %s: %w"

// Event specs use wrapper.EventSpec (id==topic). FMT-18's literal scanner
// sees the ID literal in the call and cross-checks against contract.yaml.
var (
	specEventConfigEntryUpserted = wrapper.EventSpec("event.config.entry-upserted.v1", "amqp")
	specEventConfigEntryDeleted  = wrapper.EventSpec("event.config.entry-deleted.v1", "amqp")
	specEventRoleAssigned        = wrapper.EventSpec("event.role.assigned.v1", "amqp")
	specEventRoleRevoked         = wrapper.EventSpec("event.role.revoked.v1", "amqp")
)
