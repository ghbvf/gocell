package dto

// Roles for the iotdevice example. Operators query device telemetry; the
// device itself reports back its own status.
const (
	RoleOperator = "role:operator"
	RoleDevice   = "role:device"
)
