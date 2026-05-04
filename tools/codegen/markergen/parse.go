package markergen

import (
	"errors"
	"fmt"
	"strings"
)

// splitMarker parses a single comment line and extracts the marker name and
// k=v body. Returns (name, kvLine, true) when the line is a GoCell marker
// comment, or ("", "", false) otherwise.
//
// GoCell marker syntax (adopted from controller-tools pkg/markers/parse.go
// splitMarker, L751-L773):
//
//	// +<prefix>:<name>[=<body>]
//
// Examples:
//
//	// +cell:listener:ref=cell.PrimaryListener,prefix=/api/v1   → name="cell:listener", kv="ref=cell.PrimaryListener,prefix=/api/v1"
//	// +slice:route:slice=ordercreate,subPath=/orders            → name="slice:route",   kv="slice=ordercreate,subPath=/orders"
//
// ref: kubernetes-sigs/controller-tools pkg/markers/parse.go@main (splitMarker)
func splitMarker(line string) (name, kvLine string, ok bool) {
	// Strip the comment prefix and optional leading spaces.
	s := strings.TrimSpace(line)
	if !strings.HasPrefix(s, "// +") {
		return "", "", false
	}
	s = strings.TrimPrefix(s, "// +")
	if s == "" {
		return "", "", false
	}
	// Must start with a known GoCell prefix: "cell:" or "slice:".
	if !strings.HasPrefix(s, "cell:") && !strings.HasPrefix(s, "slice:") {
		return "", "", false
	}
	// The name is everything up to (but not including) the third ':' separator
	// (the one before the k=v body). Format: <prefix>:<subname>:<kvbody>
	// e.g. "cell:listener:ref=cell.PrimaryListener,prefix=/api/v1"
	// The name part is "cell:listener"; kvbody starts after the third colon.
	//
	// Count colons: first two form the name ("cell:listener"), third separates name from kv.
	first := strings.Index(s, ":")
	if first < 0 {
		return "", "", false
	}
	second := strings.Index(s[first+1:], ":")
	if second < 0 {
		// No body (e.g. "+cell:listener" with no k=v) — treat as name with empty body.
		return s, "", true
	}
	second += first + 1
	name = s[:second]
	kvLine = s[second+1:]
	return name, kvLine, true
}

// parseKV parses a comma-separated key=value string into a map.
// Handles empty values and trims surrounding whitespace from keys and values.
// Returns an error if any segment is missing the '=' separator.
func parseKV(kvLine string) (map[string]string, error) {
	result := make(map[string]string)
	if strings.TrimSpace(kvLine) == "" {
		return result, nil
	}
	for _, segment := range strings.Split(kvLine, ",") {
		segment = strings.TrimSpace(segment)
		if segment == "" {
			continue
		}
		eq := strings.IndexByte(segment, '=')
		if eq < 0 {
			return nil, fmt.Errorf("parseKV: segment %q missing '='", segment)
		}
		key := strings.TrimSpace(segment[:eq])
		val := strings.TrimSpace(segment[eq+1:])
		result[key] = val
	}
	return result, nil
}

// errList is a non-fail-fast error accumulator.
// ref: kubernetes-sigs/controller-tools pkg/markers/collect.go (MaybeErrList)
type errList []error

// Append adds a non-nil error to the list.
func (e *errList) Append(err error) {
	if err != nil {
		*e = append(*e, err)
	}
}

// AsError returns nil when the list is empty, or an aggregated error otherwise.
// When len > 1, errors.Join is used (Go 1.20+) to preserve %w chains.
func (e errList) AsError() error {
	if len(e) == 0 {
		return nil
	}
	if len(e) == 1 {
		return e[0]
	}
	return errors.Join(e...)
}

// levenshtein computes the edit distance between strings a and b using
// classic two-row DP.
func levenshtein(a, b string) int {
	ra := []rune(a)
	rb := []rune(b)
	la, lb := len(ra), len(rb)
	if la == 0 {
		return lb
	}
	if lb == 0 {
		return la
	}
	prev := make([]int, lb+1)
	curr := make([]int, lb+1)
	for j := 0; j <= lb; j++ {
		prev[j] = j
	}
	for i := 1; i <= la; i++ {
		curr[0] = i
		for j := 1; j <= lb; j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			del := prev[j] + 1
			ins := curr[j-1] + 1
			sub := prev[j-1] + cost
			m := del
			if ins < m {
				m = ins
			}
			if sub < m {
				m = sub
			}
			curr[j] = m
		}
		prev, curr = curr, prev
	}
	return prev[lb]
}

// suggestMarkerName returns the closest known marker name when the Levenshtein
// distance is ≤ 2, otherwise returns "".
func suggestMarkerName(name string, knownMarkers []string) string {
	best := ""
	bestDist := 3 // threshold: suggest only if dist ≤ 2
	for _, k := range knownMarkers {
		d := levenshtein(name, k)
		if d < bestDist {
			bestDist = d
			best = k
		}
	}
	return best
}
