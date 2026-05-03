// Package csvparam parses comma-separated flag and query parameter values.
package csvparam

import (
	"fmt"
	"sort"
	"strings"
)

// UnknownTokenError reports that a CSV token was not in the allowed set. The
// raw token is intentionally omitted so public validation errors do not echo
// user-supplied input.
type UnknownTokenError struct {
	Param   string
	Allowed []string
}

func (e UnknownTokenError) Error() string {
	if len(e.Allowed) == 0 {
		return fmt.Sprintf("unknown %s token", e.Param)
	}
	return fmt.Sprintf("unknown %s token (valid: %s)", e.Param, strings.Join(e.Allowed, ", "))
}

// Parse splits raw on commas, trims whitespace, removes empty tokens,
// deduplicates, and returns tokens in sorted order. Blank input returns nil.
func Parse(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	seen := make(map[string]bool)
	for _, part := range strings.Split(raw, ",") {
		token := strings.TrimSpace(part)
		if token == "" {
			continue
		}
		seen[token] = true
	}
	if len(seen) == 0 {
		return nil
	}
	out := make([]string, 0, len(seen))
	for token := range seen {
		out = append(out, token)
	}
	sort.Strings(out)
	return out
}

// ParseAllowed parses raw using Parse and validates every token against allowed.
func ParseAllowed(raw string, allowed []string, param string) ([]string, error) {
	tokens := Parse(raw)
	if len(tokens) == 0 {
		return nil, nil
	}
	allowedSet := make(map[string]bool, len(allowed))
	for _, token := range allowed {
		allowedSet[token] = true
	}
	for _, token := range tokens {
		if !allowedSet[token] {
			return nil, UnknownTokenError{
				Param:   param,
				Allowed: append([]string(nil), allowed...),
			}
		}
	}
	return tokens, nil
}
