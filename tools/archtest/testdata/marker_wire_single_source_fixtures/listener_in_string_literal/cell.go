//go:build ignore_marker_wire_single_source_archtest_fixtures

// Negative fixture: cell.go contains the EXACT bytes "// +cell:listener:" but
// only inside a STRING LITERAL — there is NO real comment marker.
//
// The legacy bytes.Contains scan would FALSE-PASS because the substring is
// present. AST scan over f.Comments must reject because no *ast.Comment
// node has TrimSpace(text) starting with "// +cell:listener:".

package fakecell

const fakeMarkerHint = "// +cell:listener: not-a-real-marker"

type Cell struct{}

var _ = fakeMarkerHint
