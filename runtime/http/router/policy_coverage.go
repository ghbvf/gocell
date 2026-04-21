package router

import (
	"fmt"
	"path"
	"strings"

	kcell "github.com/ghbvf/gocell/kernel/cell"
)

// routeKey is a (method, path) pair from the chi router tree.
type routeKey struct {
	Method string
	Path   string
}

// businessMethods is the set of HTTP methods that must have an auth declaration.
// CONNECT and TRACE are excluded: they are infrastructure-level methods that
// GoCell cells never register as business handlers.
var businessMethods = map[string]bool{
	"GET":     true,
	"HEAD":    true,
	"POST":    true,
	"PUT":     true,
	"PATCH":   true,
	"DELETE":  true,
	"OPTIONS": true,
}

// verifyPolicyCoverage checks that every registered business route has an
// auth declaration (via auth.Declare). Routes that are Public, Delegated,
// or whitelisted are auto-exempted.
//
// Returns an error listing all uncovered routes. Intended to be called by
// FinalizeAuth after all Cell RegisterRoutes calls have completed.
//
// Whitelist entries support two formats:
//   - Exact: "METHOD /path" (e.g. "GET /debug/pprof")
//   - Prefix: "/path/*"     (e.g. "/debug/*" — method-agnostic prefix exemption)
//
// HEAD is auto-covered when the same path has a GET declaration (RFC 7231 §4.3.2):
// chi and stdlib ServeMux both route HEAD to the GET handler automatically, so
// requiring a separate HEAD auth.Declare would be redundant boilerplate.
//
// ref: kubernetes/apiserver — structural injection guarantees every handler
// has an authorizer; GoCell achieves the same guarantee at startup time
// by enumerating chi routes and comparing against auth.Declare metadata.
func verifyPolicyCoverage(
	registeredRoutes []routeKey,
	declaredMetas []kcell.AuthRouteMeta,
	whitelist []string,
) error {
	// Build declared set: any auth.Declare call (Public/Delegated/Policy) counts
	// as coverage. Keyed on "METHOD\x00/clean/path".
	declared := make(map[string]bool, len(declaredMetas))
	// Track GET declarations for HEAD auto-coverage (RFC 7231 §4.3.2).
	getDeclared := make(map[string]bool, len(declaredMetas))

	for _, m := range declaredMetas {
		key := strings.ToUpper(m.Method) + "\x00" + path.Clean(m.Path)
		declared[key] = true
		if strings.EqualFold(m.Method, "GET") {
			getDeclared[path.Clean(m.Path)] = true
		}
	}

	// Parse whitelist entries.
	exactWhitelist, prefixWhitelist := parseWhitelist(whitelist)

	// Walk registered routes and collect uncovered ones.
	var uncovered []string
	for _, r := range registeredRoutes {
		method := strings.ToUpper(r.Method)
		cleanedPath := path.Clean(r.Path)

		// Skip non-business methods (e.g. CONNECT from chi sub-router internals).
		if !businessMethods[method] {
			continue
		}

		// Already declared via auth.Declare.
		if declared[method+"\x00"+cleanedPath] {
			continue
		}

		// HEAD auto-covered by GET declaration (RFC 7231 §4.3.2).
		if method == "HEAD" && getDeclared[cleanedPath] {
			continue
		}

		// Whitelisted.
		if matchWhitelist(method, cleanedPath, exactWhitelist, prefixWhitelist) {
			continue
		}

		uncovered = append(uncovered, method+" "+cleanedPath)
	}

	if len(uncovered) == 0 {
		return nil
	}

	return fmt.Errorf(
		"router: %d route(s) registered without auth.Declare: [%s]; "+
			"use auth.Declare to register routes, or add to WithPolicyCoverageWhitelist if exempt",
		len(uncovered), strings.Join(uncovered, ", "),
	)
}

// parseWhitelist splits whitelist entries into exact and prefix sets.
//
// Exact entries have the format "METHOD /path" (e.g. "GET /debug/pprof").
// Prefix entries have the format "/path/*" (e.g. "/debug/*").
// All other formats are silently ignored (fail-open for whitelist parsing;
// FinalizeAuth validates business route coverage separately).
func parseWhitelist(whitelist []string) (exact map[string]bool, prefixes []string) {
	exact = make(map[string]bool, len(whitelist))
	for _, entry := range whitelist {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		// Prefix pattern: starts with '/' and ends with '/*'.
		if strings.HasPrefix(entry, "/") && strings.HasSuffix(entry, "/*") {
			// Normalize: strip the trailing '*' to get the prefix stem.
			stem := strings.TrimSuffix(entry, "*")
			prefixes = append(prefixes, stem)
			continue
		}
		// Exact pattern: "METHOD /path".
		parts := strings.SplitN(entry, " ", 2)
		if len(parts) == 2 {
			method := strings.ToUpper(strings.TrimSpace(parts[0]))
			p := path.Clean(strings.TrimSpace(parts[1]))
			exact[method+"\x00"+p] = true
		}
	}
	return exact, prefixes
}

// matchWhitelist returns true when (method, cleanPath) is covered by the
// exact whitelist or any prefix pattern.
//
// stem is the prefix stored by parseWhitelist, e.g. "/debug/" for "/debug/*".
// A cleanPath matches the stem when it equals the stem-without-trailing-slash
// (e.g. "/debug") or when it starts with the stem (e.g. "/debug/pprof").
func matchWhitelist(method, cleanPath string, exact map[string]bool, prefixes []string) bool {
	if exact[method+"\x00"+cleanPath] {
		return true
	}
	for _, stem := range prefixes {
		// stem has a trailing '/'; strip it to get the exact-match form.
		stemNoTrailing := strings.TrimSuffix(stem, "/")
		if cleanPath == stemNoTrailing || strings.HasPrefix(cleanPath, stemNoTrailing+"/") {
			return true
		}
	}
	return false
}
