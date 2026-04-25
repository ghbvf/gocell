package dto

// Roles for the iotdevice example. Operators query device telemetry; the
// device itself reports back its own status.
const (
	RoleOperator = "role:operator"
	RoleDevice   = "role:device"
	// RoleAdmin grants administrative access to the iotdevice cell endpoints.
	// Value matches the platform-wide admin role (no "role:" prefix), aligned
	// with cells/configcore/internal/dto.RoleAdmin.
	RoleAdmin = "admin"
)
