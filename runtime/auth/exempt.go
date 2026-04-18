package auth

import (
	"fmt"
	"path"
	"strings"
)

// exemptEntry captures a single compiled "METHOD /path" rule.
type exemptEntry struct {
	method   string
	template string
}

// parseExemptEntry validates and splits a single "METHOD /path" string.
func parseExemptEntry(raw string) (exemptEntry, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return exemptEntry{}, fmt.Errorf("auth: password-reset exempt entry must not be empty")
	}
	parts := strings.SplitN(raw, " ", 2)
	if len(parts) != 2 {
		return exemptEntry{}, fmt.Errorf(
			"auth: password-reset exempt entry %q must be in %q format",
			raw, "METHOD /path")
	}
	method := strings.ToUpper(strings.TrimSpace(parts[0]))
	p := strings.TrimSpace(parts[1])
	if method == "" || p == "" {
		return exemptEntry{}, fmt.Errorf("auth: password-reset exempt entry %q has empty method or path", raw)
	}
	return exemptEntry{method: method, template: path.Clean(p)}, nil
}

// CompilePasswordResetExempts compiles a list of "METHOD /path" entries into a
// method-aware predicate used by WithPasswordResetExemptMatcher. Path segments
// of the form {xxx} match any single non-empty URL segment (mirroring the
// router's template semantics), e.g. "POST /api/v1/access/users/{id}/password".
//
// Returns an error if any entry is missing the method prefix or uses an empty
// path. An empty input list returns a matcher that exempts nothing, which is
// the fail-closed default.
//
// This helper exists so the composition root (main.go / bootstrap) can own the
// list of exempt endpoints — runtime/auth no longer hard-codes cell-specific
// paths (F6 decoupling).
func CompilePasswordResetExempts(entries []string) (func(method, urlPath string) bool, error) {
	compiled := make([]exemptEntry, 0, len(entries))
	for _, raw := range entries {
		e, err := parseExemptEntry(raw)
		if err != nil {
			return nil, err
		}
		compiled = append(compiled, e)
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
