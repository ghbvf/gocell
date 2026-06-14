// Package probequery parses query parameters that gate health-probe verbose
// output. It is the single canonical source of truth for "is this request
// asking for verbose probe output?", consumed both by the health Handler
// (deciding whether to render the verbose body) and by bootstrap policies
// that gate verbose access on a header token.
package probequery

import (
	"net/http"
	"strings"
)

// Verbose reports whether the request opts in to verbose probe output via
// the ?verbose query parameter. Accepted forms: ?verbose, ?verbose=,
// ?verbose=1, ?verbose=true. All other values (false, yes, debug, …) and
// missing query parameters return false.
//
// The parser is intentionally conservative: only the three explicit truthy
// forms enable verbose mode; everything else is treated as opt-out.
func Verbose(r *http.Request) bool {
	values, ok := r.URL.Query()["verbose"]
	if !ok {
		return false
	}
	// url.ParseQuery always yields at least [""] when the key is present,
	// so we iterate values directly without a separate len==0 guard.
	for _, v := range values {
		switch strings.TrimSpace(strings.ToLower(v)) {
		case "", "1", "true":
			return true
		}
	}
	return false
}
