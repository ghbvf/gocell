package sagacoveragegen

import (
	"fmt"
	"strings"
)

// regionBounds locates the generated body between the start and end markers in
// content. It returns the byte offsets [bodyStart, bodyEnd) of the body — the
// text after the start marker's line up to (but excluding) the end marker.
// Markers must each sit on their own line.
func regionBounds(content, start, end string) (bodyStart, bodyEnd int, err error) {
	si := strings.Index(content, start)
	if si < 0 {
		return 0, 0, fmt.Errorf("sagacoveragegen: start marker not found: %q", start)
	}
	afterStart := si + len(start)
	nl := strings.IndexByte(content[afterStart:], '\n')
	if nl < 0 {
		return 0, 0, fmt.Errorf("sagacoveragegen: no newline after start marker %q", start)
	}
	bodyStart = afterStart + nl + 1
	rel := strings.Index(content[bodyStart:], end)
	if rel < 0 {
		return 0, 0, fmt.Errorf("sagacoveragegen: end marker not found after start: %q", end)
	}
	return bodyStart, bodyStart + rel, nil
}

// ExtractRegion returns the generated body between the start and end markers
// (exclusive of both marker lines). Used by the archtest to byte-compare the
// committed doc region against the rendered fragment.
func ExtractRegion(content, start, end string) (string, error) {
	bodyStart, bodyEnd, err := regionBounds(content, start, end)
	if err != nil {
		return "", err
	}
	return content[bodyStart:bodyEnd], nil
}

// ReplaceRegion replaces the body between the start and end markers with body,
// preserving the marker lines and all surrounding content. body must end with a
// newline so the end marker stays on its own line.
func ReplaceRegion(content, start, end, body string) (string, error) {
	bodyStart, bodyEnd, err := regionBounds(content, start, end)
	if err != nil {
		return "", err
	}
	return content[:bodyStart] + body + content[bodyEnd:], nil
}
