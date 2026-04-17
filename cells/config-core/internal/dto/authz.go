// Package dto provides shared handler-level data transfer objects for config-core.
package dto

// RoleAdmin is the role required to perform privileged config-core mutations.
//
// This is a cell-internal copy of access-core/internal/domain.RoleAdmin.
// Direct cross-cell import is forbidden by GoCell cell-boundary rules; both
// constants must be kept in sync manually whenever the role name changes.
const RoleAdmin = "admin"
