package metadata

import "fmt"

// http_resource_shape_validation.go is the single oracle for the #2355 owner-scoped
// (endpoints.http.resource) / self-scoped (endpoints.http.selfScoped) authoring shape,
// shared by TWO callers so they never diverge — the sibling of ValidateHTTPHeaders
// (FMT-40 + codegen) and ClassifyHTTPAuthMode (FMT-42 + codegen):
//
//   - contractgen.buildHTTPSpec — the UNBYPASSABLE codegen Hard gate: a mis-shaped
//     resource/selfScoped contract cannot be rendered regardless of entry point, so a
//     typo'd resource (e.g. "userId" when the path declares "{id}") can never generate
//     auth.RequirePermissionForResource("userId", …) — a gate that silently never matches.
//     JSON Schema cannot express map-key referential integrity (resource ∈ pathParams);
//     this oracle does.
//   - governance FMT-42 (validateFMT42ResourceShape) — the Medium validate-time arm,
//     surfacing the same violations with field-anchored fixes at `gocell validate`.
//
// DELIBERATE: there is NO "owner-scoped permission ⇒ resource required" rule (the gRPC
// FMT-41 analog). The same action (e.g. user:write) gates both owner routes (with
// resource) and admin routes (without), so resource presence is a per-route authoring
// choice, not permission-derived — see ADR 202606201500-2355 and HTTPTransportMeta.Resource.

// HTTPResourceShapeViolationKind classifies a #2355 resource/selfScoped shape violation.
type HTTPResourceShapeViolationKind int

const (
	// HTTPResourceWithoutPermission: endpoints.http.resource is set but permission is empty.
	HTTPResourceWithoutPermission HTTPResourceShapeViolationKind = iota
	// HTTPSelfScopedWithoutPermission: endpoints.http.selfScoped is true but permission is empty.
	HTTPSelfScopedWithoutPermission
	// HTTPResourceSelfScopedMutex: both resource and selfScoped are set (mutually exclusive).
	HTTPResourceSelfScopedMutex
	// HTTPResourceNotInPathParams: resource names a parameter not declared in pathParams.
	HTTPResourceNotInPathParams
)

// HTTPResourceShapeViolation is one resource/selfScoped shape problem.
type HTTPResourceShapeViolation struct {
	// Field is the offending field name: "resource" or "selfScoped" (callers map it to
	// the full endpoints.http.<field> anchor).
	Field string
	// Kind classifies the violation (callers map it to required/invalid + a fix hint).
	Kind HTTPResourceShapeViolationKind
	// Message is a human-readable description (the contractgen Hard gate surfaces it verbatim).
	Message string
}

const (
	httpResourceFieldName   = "resource"
	httpSelfScopedFieldName = "selfScoped"
)

// ValidateHTTPResourceShape returns every #2355 resource/selfScoped shape violation for
// one HTTP endpoint, in declaration order. Empty result == well-formed. A nil endpoint
// is well-formed (the kind/header gates handle nil upstream).
func ValidateHTTPResourceShape(h *HTTPTransportMeta) []HTTPResourceShapeViolation {
	if h == nil {
		return nil
	}
	var out []HTTPResourceShapeViolation
	if h.Resource != "" && h.Permission == "" {
		out = append(out, HTTPResourceShapeViolation{
			httpResourceFieldName, HTTPResourceWithoutPermission,
			fmt.Sprintf("sets endpoints.http.resource %q without endpoints.http.permission; "+
				"an owner-scoped gate still requires an action", h.Resource),
		})
	}
	if h.SelfScoped && h.Permission == "" {
		out = append(out, HTTPResourceShapeViolation{
			httpSelfScopedFieldName, HTTPSelfScopedWithoutPermission,
			"sets endpoints.http.selfScoped without endpoints.http.permission; " +
				"a self-scoped gate still requires an action",
		})
	}
	if h.Resource != "" && h.SelfScoped {
		out = append(out, HTTPResourceShapeViolation{
			httpResourceFieldName, HTTPResourceSelfScopedMutex,
			"sets both endpoints.http.resource and endpoints.http.selfScoped, which are " +
				"mutually exclusive (owner-scoped path-param resource vs self-scoped subject)",
		})
	}
	// Referential integrity: resource MUST name a declared path parameter. A typo
	// (e.g. "userId" when the path template declares "{id}") otherwise silently produces
	// a gate whose path-param lookup returns "" — an owner gate that can never match.
	if h.Resource != "" {
		if _, ok := h.PathParams[h.Resource]; !ok {
			out = append(out, HTTPResourceShapeViolation{
				httpResourceFieldName, HTTPResourceNotInPathParams,
				fmt.Sprintf("endpoints.http.resource %q is not declared in endpoints.http.pathParams", h.Resource),
			})
		}
	}
	return out
}
