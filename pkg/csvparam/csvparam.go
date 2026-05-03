// Package csvparam parses comma-separated flag and query parameter values.
package csvparam

import (
	"fmt"
	"sort"
	"strings"
)

// UnknownTokenError reports that one or more CSV tokens were not in the allowed
// set. The raw tokens are intentionally omitted from the Error() message so
// public validation errors do not echo user-supplied input (XSS / log injection
// prevention). Use the Tokens field for structured logging only.
type UnknownTokenError struct {
	Param   string
	Allowed []string
	// Tokens holds the unknown input values. They are excluded from the Error()
	// message — consume via structured slog fields, never inline into responses.
	Tokens []string
}

func (e UnknownTokenError) Error() string {
	n := len(e.Tokens)
	if n == 0 {
		n = 1 // conservative: at least one token was rejected
	}
	if len(e.Allowed) == 0 {
		return fmt.Sprintf("invalid %s: %d token(s) not allowed", e.Param, n)
	}
	return fmt.Sprintf("invalid %s: %d token(s) not allowed (valid: %s)", e.Param, n, strings.Join(e.Allowed, ", "))
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
// All unknown tokens are collected before returning the error so callers receive
// the full set of violations in a single pass.
func ParseAllowed(raw string, allowed []string, param string) ([]string, error) {
	tokens := Parse(raw)
	if len(tokens) == 0 {
		return nil, nil
	}
	allowedSet := make(map[string]bool, len(allowed))
	for _, token := range allowed {
		allowedSet[token] = true
	}
	var unknown []string
	for _, token := range tokens {
		if !allowedSet[token] {
			unknown = append(unknown, token)
		}
	}
	if len(unknown) > 0 {
		return nil, UnknownTokenError{
			Param:   param,
			Allowed: append([]string(nil), allowed...),
			Tokens:  unknown,
		}
	}
	return tokens, nil
}
