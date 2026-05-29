package cellvocab

// ValidRolesForKind returns the legal ContractRoles for a given ContractKind.
//
//	http:       serve, call
//	event:      publish, subscribe
//	command:    handle, invoke
//	projection: provide, read
//	grpc:       serve, call
//
// An unknown or future kind returns nil (fail-open); callers must check for nil
// before ranging and must not treat nil as "kind is valid with no roles".
func ValidRolesForKind(kind ContractKind) []ContractRole {
	switch kind {
	case ContractHTTP:
		return []ContractRole{RoleServe, RoleCall}
	case ContractEvent:
		return []ContractRole{RolePublish, RoleSubscribe}
	case ContractCommand:
		return []ContractRole{RoleHandle, RoleInvoke}
	case ContractProjection:
		return []ContractRole{RoleProvide, RoleRead}
	case ContractGRPC:
		return []ContractRole{RoleServe, RoleCall}
	default:
		return nil
	}
}

// IsProviderRole returns true if role is a provider-side role
// (serve, publish, handle, provide).
func IsProviderRole(role ContractRole) bool {
	switch role {
	case RoleServe, RolePublish, RoleHandle, RoleProvide:
		return true
	default:
		return false
	}
}

// IsConsumerRole returns true if role is a consumer-side role
// (call, subscribe, invoke, read).
func IsConsumerRole(role ContractRole) bool {
	switch role {
	case RoleCall, RoleSubscribe, RoleInvoke, RoleRead:
		return true
	default:
		return false
	}
}
