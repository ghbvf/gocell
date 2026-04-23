// Package dto provides shared handler-level data transfer objects for configcore.
package dto

// RoleAdmin is the role required to perform privileged configcore mutations.
//
// This is a cell-internal copy of accesscore/internal/domain.RoleAdmin.
// Direct cross-cell import is forbidden by GoCell cell-boundary rules; both
// constants must be kept in sync manually whenever the role name changes.
const RoleAdmin = "admin"
