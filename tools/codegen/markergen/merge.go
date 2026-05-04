package markergen

import (
	"github.com/ghbvf/gocell/kernel/metadata"
)

// Merge scans cell.go marker comments under projectRoot, extracts
// listener / route / subscribe declarations, and projects them into a
// per-cell WireBundle map keyed by cell ID.
//
// Drift detection: if cell.yaml.listeners or slice.yaml.{routeMounts,
// subscribes} fields are present alongside markers, Merge returns an error
// — marker is the single source of truth for wire facts (K#05).
//
// W1 RED stub: returns an empty map and a not-implemented error to lock
// the public signature for archtest signature verification. The real
// implementation lands in W2 (parse.go / collect.go / merge.go).
func Merge(projectRoot string, project *metadata.ProjectMeta) (map[string]WireBundle, error) {
	_ = projectRoot
	_ = project
	return map[string]WireBundle{}, errNotImplemented
}

// errNotImplemented is the sentinel returned by the W1 stub.
var errNotImplemented = mergeNotImplementedError{}

type mergeNotImplementedError struct{}

func (mergeNotImplementedError) Error() string {
	return "markergen.Merge: W1 stub — implementation lands in W2"
}
