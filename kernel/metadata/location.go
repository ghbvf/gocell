// ref: gopkg.in/yaml.v3 — Node.Line and Node.Column provide 1-based source
// positions filled by libyaml during parsing (yaml.go, type Node struct).
// ref: github.com/goccy/go-yaml/path.go — AST shape (rootNode / selectorNode
// / indexNode) used as inspiration for the path grammar below. We adopt the
// node-by-node walk idea but write our own grammar because (a) kernel/ is
// limited to yaml.v3 + stdlib (no third-party imports), and (b) GoCell only
// needs a strict subset of JSONPath (no "$", "[*]", ".." or quoted idents).

package metadata

import (
	"fmt"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// Position is a 1-based (line, column) reference into a YAML source file.
// A zero Position (Line==0 or Column==0) means "unknown location".
type Position struct {
	Line   int
	Column int
}

// Known reports whether the position carries usable line/column information.
// Zero values are treated as unknown because yaml.v3 reports positions as
// 1-based, so line 0 cannot occur for a parsed node.
func (p Position) Known() bool {
	return p.Line > 0 && p.Column > 0
}

// Find walks `root` (a yaml.Node, typically a *DocumentNode returned by
// yaml.Decoder) along the dotted path and returns the matching leaf node.
//
// Path grammar (a restricted subset of JSONPath):
//
//	path    = segment ("." segment)*
//	segment = ident ("[" uint "]")*
//	ident   = [A-Za-z_][A-Za-z0-9_-]*
//
// Examples: "id", "owner.team", "slices[0].contractUsages[1].contract",
// "matrix[0][1]".
//
// Unsupported (by design): leading "$", wildcards "[*]", recursive "..",
// quoted identifiers. We walk the minimum grammar the validator needs.
//
// Error conditions: empty/invalid path, missing field, index out of range,
// type mismatch (indexing a mapping or keying a sequence), empty document.
// The path prefix that reached the offending step is included in the error.
func Find(root *yaml.Node, path string) (*yaml.Node, error) {
	if root == nil {
		return nil, fmt.Errorf("metadata: Find on nil node")
	}
	segs, err := parsePath(path)
	if err != nil {
		return nil, fmt.Errorf("metadata: invalid path %q: %w", path, err)
	}

	cur := root
	if cur.Kind == yaml.DocumentNode {
		if len(cur.Content) == 0 {
			return nil, fmt.Errorf("metadata: Find on empty document")
		}
		cur = cur.Content[0]
	}

	for i, seg := range segs {
		cur, err = stepField(cur, seg.field)
		if err != nil {
			return nil, fmt.Errorf("metadata: at %q: %w", pathUpTo(segs, i, -1), err)
		}
		for j, idx := range seg.indices {
			cur, err = stepIndex(cur, idx)
			if err != nil {
				return nil, fmt.Errorf("metadata: at %q: %w", pathUpTo(segs, i, j), err)
			}
		}
	}
	return cur, nil
}

// Locate is a best-effort convenience that returns the (Line, Column) of the
// node at `path`, or the zero Position if the path cannot be resolved.
// Callers that need a precise error should use Find directly.
//
// The caller is expected to inspect pos.Known() rather than comparing Line
// against 0 directly: yaml.v3 emits 1-based positions, so a zero Line here
// unambiguously means "unresolved", never "line 0".
func Locate(root *yaml.Node, path string) Position {
	n, err := Find(root, path)
	if err != nil || n == nil {
		return Position{}
	}
	return Position{Line: n.Line, Column: n.Column}
}

// --- internal ---

type pathSegment struct {
	field   string
	indices []int
}

func parsePath(p string) ([]pathSegment, error) {
	if p == "" {
		return nil, fmt.Errorf("empty path")
	}
	parts := strings.Split(p, ".")
	out := make([]pathSegment, 0, len(parts))
	for _, raw := range parts {
		seg, err := parseSegment(raw)
		if err != nil {
			return nil, err
		}
		out = append(out, seg)
	}
	return out, nil
}

func parseSegment(s string) (pathSegment, error) {
	var seg pathSegment
	if s == "" {
		return seg, fmt.Errorf("empty segment")
	}
	lb := strings.IndexByte(s, '[')
	if lb < 0 {
		if !isIdent(s) {
			return seg, fmt.Errorf("invalid identifier %q", s)
		}
		seg.field = s
		return seg, nil
	}
	seg.field = s[:lb]
	if !isIdent(seg.field) {
		return seg, fmt.Errorf("invalid identifier %q", seg.field)
	}
	rest := s[lb:]
	for len(rest) > 0 {
		if rest[0] != '[' {
			return seg, fmt.Errorf("expected '[' in segment %q", s)
		}
		rb := strings.IndexByte(rest, ']')
		if rb < 0 {
			return seg, fmt.Errorf("unclosed '[' in segment %q", s)
		}
		inner := rest[1:rb]
		if inner == "" {
			return seg, fmt.Errorf("empty index in segment %q", s)
		}
		n, err := strconv.Atoi(inner)
		if err != nil || n < 0 {
			return seg, fmt.Errorf("invalid index %q in segment %q", inner, s)
		}
		seg.indices = append(seg.indices, n)
		rest = rest[rb+1:]
	}
	return seg, nil
}

// isIdent checks [A-Za-z_][A-Za-z0-9_-]*.
func isIdent(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		switch {
		case r >= 'a' && r <= 'z',
			r >= 'A' && r <= 'Z',
			r == '_':
			continue
		case r >= '0' && r <= '9', r == '-':
			if i == 0 {
				return false
			}
		default:
			return false
		}
	}
	return true
}

func stepField(n *yaml.Node, key string) (*yaml.Node, error) {
	if n.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("expected mapping to read field %q, got kind %d", key, n.Kind)
	}
	// yaml.v3 MappingNode stores [k0, v0, k1, v1, ...] in Content.
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			return n.Content[i+1], nil
		}
	}
	return nil, fmt.Errorf("field %q not found", key)
}

func stepIndex(n *yaml.Node, idx int) (*yaml.Node, error) {
	if n.Kind != yaml.SequenceNode {
		return nil, fmt.Errorf("expected sequence for index [%d], got kind %d", idx, n.Kind)
	}
	if idx >= len(n.Content) {
		return nil, fmt.Errorf("index %d out of range (length %d)", idx, len(n.Content))
	}
	return n.Content[idx], nil
}

// pathUpTo reconstructs the path string up to (and including) segs[i] and
// optionally the first stopIdx+1 indices within that segment. Pass stopIdx=-1
// to include no indices for segs[i] (field-step error).
func pathUpTo(segs []pathSegment, i, stopIdx int) string {
	var b strings.Builder
	for k := range i {
		if k > 0 {
			b.WriteByte('.')
		}
		writeSegment(&b, segs[k], -1)
	}
	if i > 0 {
		b.WriteByte('.')
	}
	writeSegment(&b, segs[i], stopIdx)
	return b.String()
}

func writeSegment(b *strings.Builder, s pathSegment, stopIdx int) {
	b.WriteString(s.field)
	limit := len(s.indices)
	if stopIdx >= 0 && stopIdx+1 < limit {
		limit = stopIdx + 1
	}
	for k := range limit {
		fmt.Fprintf(b, "[%d]", s.indices[k])
	}
}
