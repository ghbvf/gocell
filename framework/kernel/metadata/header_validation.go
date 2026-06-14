package metadata

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// httpHeaderNameRe matches a canonical HTTP request-header name that produces a
// clean Go identifier via contractgen.goPascalCase (which splits on "-"): a
// letter followed by letters / digits / hyphens, e.g. "X-Tenant-ID",
// "Authorization", "Content-Type". Rejects names with spaces, colons, or other
// token-illegal characters that would break the r.Header.Get literal or yield a
// malformed generated field name.
var httpHeaderNameRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9-]*$`)

// HeaderViolationKind classifies an endpoints.http.headers schema violation.
type HeaderViolationKind int

const (
	// HeaderViolationName — header name is not a canonical HTTP token.
	HeaderViolationName HeaderViolationKind = iota
	// HeaderViolationType — type is missing or not "string". Headers are
	// populate-only at codegen (the generated handler emits
	// req.X = r.Header.Get(name), which yields a string); a non-string type
	// would generate an uncompilable typed field assignment (#1494 review F1).
	HeaderViolationType
	// HeaderViolationConstraint — minLength/maxLength/minimum/maximum declared.
	// Headers emit no validation gate, so a length/numeric constraint would
	// silently no-op; reject it rather than mislead.
	HeaderViolationConstraint
	// HeaderViolationDuplicate — case-insensitive duplicate header name. HTTP
	// header names are case-insensitive, so two keys folding to the same
	// canonical name collide (#1494 review F3).
	HeaderViolationDuplicate
)

// HeaderViolation is one endpoints.http.headers schema problem.
type HeaderViolation struct {
	// Header is the offending declared header name (the contract.yaml map key).
	Header string
	// Kind classifies the violation.
	Kind HeaderViolationKind
	// Message is a human-readable description of the problem.
	Message string
}

// ValidateHTTPHeaders is the SINGLE SOURCE OF TRUTH for endpoints.http.headers
// schema validity. Headers are populate-only at codegen — the generated handler
// emits `req.X = r.Header.Get("<name>")` with no validation gate — so the
// declarable shape is restricted to what that accessor can express:
//
//   - name: a canonical HTTP token (httpHeaderNameRe)
//   - type: "string" only — r.Header.Get returns string; integer/number/boolean
//     would generate an uncompilable typed field (#1494 review F1)
//   - no minLength/maxLength/minimum/maximum — no gate is generated, so they
//     would silently no-op
//   - no case-insensitive duplicate name — HTTP header names are case-insensitive
//     (#1494 review F3)
//
// Both governance FMT-40 (validate-time) and contractgen buildHTTPSpec
// (codegen-time, fail-closed) call this so the two gates cannot drift: a header
// that governance would reject can never reach the generator, and the generator
// fails closed even if `gocell validate` is skipped (#1494 review F4).
//
// Returns violations in a deterministic order (by header name, then kind); an
// empty slice means the header set is valid.
func ValidateHTTPHeaders(headers map[string]ParamSchema) []HeaderViolation {
	if len(headers) == 0 {
		return nil
	}

	names := make([]string, 0, len(headers))
	for name := range headers {
		names = append(names, name)
	}
	sort.Strings(names)

	// Case-insensitive duplicate detection: group declared names by canonical
	// (lowercased) form; any group with >1 member is a duplicate set.
	canonical := make(map[string][]string, len(headers))
	for _, name := range names {
		lc := strings.ToLower(name)
		canonical[lc] = append(canonical[lc], name)
	}

	var out []HeaderViolation
	for _, name := range names {
		out = append(out, perHeaderViolations(name, headers[name])...)

		// Report the lexicographically-later member(s) of a canonical group as the
		// duplicate(s) of the first; the first member is the surviving declaration.
		if grp := canonical[strings.ToLower(name)]; len(grp) > 1 && grp[0] != name {
			out = append(out, HeaderViolation{
				Header: name, Kind: HeaderViolationDuplicate,
				Message: fmt.Sprintf(
					"header %q is a case-insensitive duplicate of %q (HTTP header names are case-insensitive)", name, grp[0],
				),
			})
		}
	}

	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Header != out[j].Header {
			return out[i].Header < out[j].Header
		}
		return out[i].Kind < out[j].Kind
	})
	return out
}

// perHeaderViolations validates a single header's name + value shape (name token,
// type: string, no length/numeric constraint). Case-insensitive duplicate
// detection is a set-level concern handled by the caller.
func perHeaderViolations(name string, h ParamSchema) []HeaderViolation {
	var out []HeaderViolation
	add := func(k HeaderViolationKind, msg string) {
		out = append(out, HeaderViolation{Header: name, Kind: k, Message: msg})
	}

	if !httpHeaderNameRe.MatchString(name) {
		add(HeaderViolationName, fmt.Sprintf("header name %q is not a valid HTTP header token", name))
	}

	switch {
	case h.Type == "":
		add(HeaderViolationType, fmt.Sprintf(
			"header %q declares no type; headers are populate-only and must declare type: string", name,
		))
	case h.Type != "string":
		add(HeaderViolationType, fmt.Sprintf(
			"header %q type %q is unsupported; headers are populate-only at codegen and only type: string "+
				"can be generated", name, h.Type,
		))
	}

	if h.MinLength != nil || h.MaxLength != nil || h.Minimum != nil || h.Maximum != nil {
		add(HeaderViolationConstraint, fmt.Sprintf(
			"header %q declares minLength/maxLength/minimum/maximum, which are not codegen-enforced "+
				"(headers are populate-only — the generated handler emits no gate)", name,
		))
	}

	return out
}
