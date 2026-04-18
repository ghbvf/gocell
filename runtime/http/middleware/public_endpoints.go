package middleware

// ref: Go 1.22 net/http ServeMux pattern grammar "[METHOD] PATH"
// Adopted: "METHOD /path" mandatory format; unknown entries cause fail-fast error.
// Deviated: HEAD alias for GET entries (RFC 7231 §4.3.2 — HEAD is the safe/
// idempotent variant of GET, stdlib ServeMux and chi v5 both treat GET as
// implicitly covering HEAD, so GoCell follows the same convention).
//
// ref: otelhttp WithPublicEndpointFn per-request predicate
// Adopted: the compiled result is a func(*http.Request) bool predicate that
// all three trust-boundary consumers (auth bypass / tracing new-root /
// request-id reject) already accept — no downstream API change needed.

import (
	"errors"
	"fmt"
	"net/http"
	"path"
	"strings"
)

// validMethods is the set of recognized HTTP methods for public endpoint entries.
// Entries with a method not in this set are rejected at compile time (fail-fast).
var validMethods = map[string]bool{
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

// matchKey builds the lookup key used in the compiled set.
// Uses \x00 as separator because neither HTTP method nor URL path may contain
// a NUL byte, so the key is unambiguous and easy to inspect in debug output.
func matchKey(method, cleanPath string) string {
	return method + "\x00" + cleanPath
}

// CompilePublicEndpoints parses a slice of "METHOD /path" entries and returns
// a per-request predicate that returns true when the request's (method, path)
// pair is in the public set.
//
// Intended for router internals (applyPublicEndpoints) and bootstrap-layer
// preflight validation (WithPublicEndpoints). Cell code should not call this
// directly — use bootstrap.WithPublicEndpoints as the composition-root API.
//
// Returns a non-nil error aggregating all malformed or duplicate entries via
// errors.Join — the caller should treat any error as a startup failure.
//
// Rules:
//   - Entry format: "METHOD /path" (single space minimum; extra spaces trimmed).
//   - Method is normalised to uppercase.
//   - Path is normalised with path.Clean; must start with '/'.
//   - Entries with no method prefix are rejected (fail-fast; no silent fallback).
//   - Method must be one of: GET/HEAD/POST/PUT/PATCH/DELETE/OPTIONS/CONNECT/TRACE.
//   - GET entries automatically also match HEAD (RFC 7231 §4.3.2).
//   - Duplicate (method, path) pairs return an error (protect config cleanliness).
//   - Empty entry slice is valid; the returned predicate always returns false.
//
// ref: Go 1.22 net/http ServeMux pattern grammar "[METHOD] PATH"
// ref: otelhttp WithPublicEndpointFn per-request predicate
func CompilePublicEndpoints(entries []string) (func(*http.Request) bool, error) {
	set := make(map[string]bool, len(entries)*2)
	var errs []error

	for i, raw := range entries {
		method, cleanPath, err := parseEntry(raw)
		if err != nil {
			errs = append(errs, fmt.Errorf("entry[%d]: %w", i, err))
			continue
		}

		key := matchKey(method, cleanPath)
		if set[key] {
			errs = append(errs, fmt.Errorf("entry[%d]: duplicate (method=%s path=%s)", i, method, cleanPath))
			continue
		}
		set[key] = true

		// GET → HEAD alias: RFC 7231 §4.3.2 — HEAD is a safe/idempotent subset of
		// GET; stdlib ServeMux and chi v5 both route HEAD to the GET handler
		// automatically. We extend the same semantic to the trust-boundary matcher
		// so that "GET /api/v1/.well-known/jwks" also covers HEAD pre-flight checks.
		if method == http.MethodGet {
			headKey := matchKey(http.MethodHead, cleanPath)
			if set[headKey] {
				errs = append(errs, fmt.Errorf("entry[%d]: duplicate — GET auto-alias HEAD conflicts with existing HEAD %s entry", i, cleanPath))
				continue
			}
			set[headKey] = true
		}
	}

	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}

	return func(r *http.Request) bool {
		key := matchKey(strings.ToUpper(r.Method), path.Clean(r.URL.Path))
		return set[key]
	}, nil
}

// parseEntry splits a raw "METHOD /path" string into its normalised components.
func parseEntry(raw string) (method, cleanPath string, err error) {
	parts := strings.SplitN(raw, " ", 2)
	if len(parts) != 2 {
		return "", "", fmt.Errorf(
			"public endpoint entry %q: must be \"METHOD /path\" (e.g. \"POST /api/v1/auth/login\")", raw)
	}

	method = strings.ToUpper(strings.TrimSpace(parts[0]))
	rawPath := strings.TrimSpace(parts[1])

	if method == "" {
		return "", "", fmt.Errorf(
			"public endpoint entry %q: method must not be empty", raw)
	}
	if !validMethods[method] {
		return "", "", fmt.Errorf(
			"public endpoint entry %q: method %q not recognized (must be one of GET/HEAD/POST/PUT/PATCH/DELETE/OPTIONS/CONNECT/TRACE)", raw, method)
	}
	if rawPath == "" || rawPath[0] != '/' {
		return "", "", fmt.Errorf(
			"public endpoint entry %q: path must start with '/' (got %q)", raw, rawPath)
	}

	return method, path.Clean(rawPath), nil
}
