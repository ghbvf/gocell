package cell

// Policy is an opaque marker for route-group authentication/authorization
// policies. Concrete implementations live in runtime/bootstrap to keep
// kernel layer free of HTTP middleware semantics. Cells consume policies
// as bootstrap.PolicyNone(), bootstrap.PolicyJWT(...), etc.
//
// The interface is intentionally minimal: kernel knows only that a Policy
// can describe itself for startup logging and introspection. The actual
// middleware wiring is performed by runtime/bootstrap's internal
// mountablePolicy extension of this interface.
type Policy interface {
	// Describe returns a human-readable policy name (for startup logging
	// and archtest introspection). Does not include secrets.
	Describe() string
}
