package ports

// HealthCheckable is an optional interface for infrastructure components that
// can report their health status. Repository implementations should implement
// this when they depend on external resources (database, cache) whose
// availability should be reflected in /readyz.
//
// This is intentionally separate from domain repository interfaces (e.g.
// SessionRepository) because health checking is an infrastructure concern,
// not a domain concern. However, keeping it in ports makes the expectation
// explicit: future real adapters implementing SessionRepository SHOULD also
// implement HealthCheckable so that session store availability is observable.
type HealthCheckable interface {
	Health() error
}
