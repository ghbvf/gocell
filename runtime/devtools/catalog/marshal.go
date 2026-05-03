// Package catalog — marshal.go: MarshalDocument and format serialization.
package catalog

import (
	"encoding/json"
	"fmt"

	"gopkg.in/yaml.v3"
)

// MarshalDocument serializes d as JSON or YAML according to format. Returns
// an error for unrecognized format strings.
//
//   - format == "json" → encoding/json with two-space indent
//   - format == "yaml" → gopkg.in/yaml.v3 default indent
//
// Both formats are byte-deterministic for identical input thanks to:
//   - Document field declaration order (struct fields encoded in source order)
//   - kerneldepgraph.Graph.MarshalJSON sorting Packages + Imports
//   - BuildDocument sorting Entities and Relations before returning
func MarshalDocument(d Document, format string) ([]byte, error) {
	switch format {
	case "json":
		return json.MarshalIndent(d, "", "  ")
	case "yaml":
		return yaml.Marshal(d)
	default:
		return nil, fmt.Errorf("catalog.MarshalDocument: unrecognized format %q (want json|yaml)", format)
	}
}
