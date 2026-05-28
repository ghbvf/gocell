package cellvocab

// ValidRolesForKind returns the legal ContractRoles for a given ContractKind.
//
//	http:       serve, call
//	event:      publish, subscribe
//	command:    handle, invoke
//	projection: provide, read
//	webhook:    webhook-receive, webhook-dispatch
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
	case ContractWebhook:
		return []ContractRole{RoleWebhookReceive, RoleWebhookDispatch}
	default:
		return nil
	}
}

// IsProviderRole returns true if role is a provider-side role
// (serve, publish, handle, provide, webhook-dispatch). webhook-dispatch is the
// outbound side — the cell produces/pushes webhooks to an external target.
func IsProviderRole(role ContractRole) bool {
	switch role {
	case RoleServe, RolePublish, RoleHandle, RoleProvide, RoleWebhookDispatch:
		return true
	default:
		return false
	}
}

// IsConsumerRole returns true if role is a consumer-side role
// (call, subscribe, invoke, read, webhook-receive). webhook-receive is the
// inbound side — the cell consumes webhooks delivered by an external source.
func IsConsumerRole(role ContractRole) bool {
	switch role {
	case RoleCall, RoleSubscribe, RoleInvoke, RoleRead, RoleWebhookReceive:
		return true
	default:
		return false
	}
}
