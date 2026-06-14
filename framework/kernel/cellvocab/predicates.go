package cellvocab

// ValidRolesForKind returns the legal ContractRoles for a given ContractKind.
//
//	http:       serve, call
//	event:      publish, subscribe
//	command:    handle, invoke
//	projection: provide, read
//	webhook:    webhook-receive, webhook-dispatch
//	grpc:       serve, call
//	saga:       orchestrate, subscribe
//
// subscribe is legal on a saga contract ONLY as a saga-journal projection source
// (EPIC #1609 PR-05): the projecting cell consumes the saga's terminal journal as
// a read-model input stream. A bare subscribe-on-saga without a projection +
// projectionSource=saga-journal is rejected by governance rule
// PROJECTION-SAGA-SOURCE-NEEDS-PROJECTION-01 (the saga journal is the only legal
// subscribe target — there is no saga outbox topic).
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
	case ContractWebhook:
		return []ContractRole{RoleWebhookReceive, RoleWebhookDispatch}
	case ContractGRPC:
		return []ContractRole{RoleServe, RoleCall}
	case ContractSaga:
		return []ContractRole{RoleOrchestrate, RoleSubscribe}
	default:
		return nil
	}
}

// IsProviderRole returns true if role is a provider-side role
// (serve, publish, handle, provide, orchestrate, webhook-dispatch). orchestrate
// is the saga provider; webhook-dispatch is the outbound side — the cell
// produces/pushes webhooks to an external target.
func IsProviderRole(role ContractRole) bool {
	switch role {
	case RoleServe, RolePublish, RoleHandle, RoleProvide, RoleOrchestrate, RoleWebhookDispatch:
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
