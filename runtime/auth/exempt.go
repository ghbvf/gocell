package auth

import (
	"errors"
	"fmt"
	"net/http"
	"path"
	"strings"
)

// validExemptMethods mirrors runtime/http/middleware.validMethods so that
// password-reset exempt entries accept the same whitelisted HTTP methods as
// public-endpoint entries. The two lists deliberately agree — a divergence
// would mean one compile helper silently accepts strings the other rejects,
// which is how the original weaker exempt validation shipped to begin with.
var validExemptMethods = map[string]bool{
	http.MethodGet:     true,
	http.MethodHead:    true,
	http.MethodPost:    true,
	http.MethodPut:     true,
	http.MethodPatch:   true,
	http.MethodDelete:  true,
	http.MethodOptions: true,
	http.MethodConnect: true,
	http.MethodTrace:   true,
}

// exemptEntry captures a single compiled "METHOD /path" rule. Path may contain
// {name} segments that match any single non-empty URL segment (router-aligned
// template semantics), so exact-match compilation is not suitable here — this
// is why the exempt list cannot share middleware.CompilePublicEndpoints' set
// lookup verbatim and keeps its own linear template matcher instead.
type exemptEntry struct {
	method   string
	template string
}

// parseExemptEntry validates and splits a single "METHOD /path" string using
// the same fail-fast rules as middleware.CompilePublicEndpoints: method must
// be in the HTTP whitelist, path must be absolute, both components non-empty.
func parseExemptEntry(raw string) (exemptEntry, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return exemptEntry{}, fmt.Errorf(
			"auth: password-reset exempt entry must not be empty")
	}
	parts := strings.SplitN(raw, " ", 2)
	if len(parts) != 2 {
		return exemptEntry{}, fmt.Errorf(
			"auth: password-reset exempt entry %q: must be %q format (e.g. %q)",
			raw, "METHOD /path", "POST /path/{id}/action")
	}
	method := strings.ToUpper(strings.TrimSpace(parts[0]))
	rawPath := strings.TrimSpace(parts[1])

	if method == "" {
		return exemptEntry{}, fmt.Errorf(
			"auth: password-reset exempt entry %q: method must not be empty", raw)
	}
	if !validExemptMethods[method] {
		return exemptEntry{}, fmt.Errorf(
			"auth: password-reset exempt entry %q: method %q not recognized (must be one of GET/HEAD/POST/PUT/PATCH/DELETE/OPTIONS/CONNECT/TRACE)",
			raw, method)
	}
	if rawPath == "" || rawPath[0] != '/' {
		return exemptEntry{}, fmt.Errorf(
			"auth: password-reset exempt entry %q: path must start with '/' (got %q)", raw, rawPath)
	}

	return exemptEntry{method: method, template: path.Clean(rawPath)}, nil
}

// CompilePasswordResetExempts compiles a list of "METHOD /path" entries into a
// method-aware predicate used by WithPasswordResetExemptMatcher. Path segments
// of the form {xxx} match any single non-empty URL segment (mirroring the
// router's template semantics), e.g. "POST /api/v1/access/users/{id}/password".
//
// Validation matches middleware.CompilePublicEndpoints fail-fast strength
// (reviewer finding — prior divergence allowed weaker exempt configs to slip
// through startup):
//
//   - entry must be in "METHOD /path" format, non-empty on both sides
//   - method must be one of GET/HEAD/POST/PUT/PATCH/DELETE/OPTIONS/CONNECT/TRACE
//   - path must start with '/'
//   - duplicate (method, template) pairs are rejected
//
// Errors across the batch are aggregated via errors.Join so the operator sees
// every malformed entry in a single startup failure rather than whack-a-mole.
//
// An empty input list returns a matcher that exempts nothing — the fail-closed
// default that middleware enforces before callers wire a matcher.
//
// This helper exists so the composition root (main.go / bootstrap) owns the
// list of exempt endpoints — runtime/auth no longer hard-codes cell-specific
// paths (F6 decoupling).
func CompilePasswordResetExempts(entries []string) (func(method, urlPath string) bool, error) {
	compiled := make([]exemptEntry, 0, len(entries))
	seen := make(map[string]bool, len(entries))
	var errs []error
	for i, raw := range entries {
		e, err := parseExemptEntry(raw)
		if err != nil {
			errs = append(errs, fmt.Errorf("entry[%d]: %w", i, err))
			continue
		}
		key := e.method + "\x00" + e.template
		if seen[key] {
			errs = append(errs, fmt.Errorf(
				"entry[%d]: duplicate (method=%s path=%s)", i, e.method, e.template))
			continue
		}
		seen[key] = true
		compiled = append(compiled, e)
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return func(method, urlPath string) bool {
		cleaned := path.Clean(urlPath)
		for _, e := range compiled {
			if e.method == method && matchPathTemplate(e.template, cleaned) {
				return true
			}
		}
		return false
	}, nil
}
