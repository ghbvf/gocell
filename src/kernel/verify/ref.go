package verify

import (
	"fmt"
	"strings"

	"github.com/ghbvf/gocell/pkg/errcode"
)

// resolvedRef holds the parsed components of a verify reference string.
type resolvedRef struct {
	Kind       string // "journey", "smoke", "unit", "contract"
	Pkg        string // Go test package path; empty means caller-provided
	RunPattern string // CamelCase -run regex pattern
}

// resolveRef parses a structured ref like "journey.J-sso-login.session-revoke"
// and returns the resolved go test package and -run pattern.
//
// Supported formats:
//
//	journey.{journeyID}.{suffix} → pkg="./journeys/...", pattern=CamelCase(journeyID)+CamelCase(suffix)
//	smoke.{cellID}.{suffix}     → pkg="./cells/{cellID}/...", pattern=CamelCase(suffix)
//	unit.{scope}.{suffix}       → pkg="" (caller provides), pattern=CamelCase(suffix)
//	contract.{id...}.{role}     → pkg="" (caller provides), pattern=CamelCase(role)
//
// Returns ErrCheckRefInvalid for unrecognized or malformed refs.
//
// ref: kubernetes pkg/apis validation — segment-level input validation.
func resolveRef(ref string) (resolvedRef, error) {
	parts := strings.SplitN(ref, ".", 3)
	if len(parts) < 3 || parts[1] == "" || parts[2] == "" {
		return resolvedRef{}, errcode.New(errcode.ErrCheckRefInvalid,
			fmt.Sprintf("ref %q must have at least 3 non-empty dot-separated segments", ref))
	}

	prefix := parts[0]
	suffix := parts[2] // everything after second dot

	switch prefix {
	case PrefixJourney:
		// Journey tests may live in ./journeys/... or ./tests/integration/...
		// The Runner resolves the actual path at execution time.
		// Include journeyID in pattern to disambiguate refs with identical suffixes
		// (e.g., event-publish appears across multiple journeys).
		return resolvedRef{
			Kind:       PrefixJourney,
			Pkg:        "", // resolved by Runner based on project layout
			RunPattern: kebabToCamelCase(parts[1]) + kebabToCamelCase(suffix),
		}, nil

	case PrefixSmoke:
		cellID := parts[1]
		if err := validateSegment(cellID, "smoke cellID"); err != nil {
			return resolvedRef{}, err
		}
		return resolvedRef{
			Kind:       PrefixSmoke,
			Pkg:        fmt.Sprintf("./cells/%s/...", cellID),
			RunPattern: kebabToCamelCase(suffix),
		}, nil

	case PrefixUnit:
		return resolvedRef{
			Kind:       PrefixUnit,
			RunPattern: kebabToCamelCase(suffix),
		}, nil

	case PrefixContract:
		// contract.{contractID}.{role} — encode parts[1] + suffix into RunPattern
		// to avoid matching unrelated tests. SplitN(ref,3) puts the second segment
		// in parts[1], so we must include it. Each dot-segment is independently
		// converted: "contract.http.auth.login.v1.serve" → "HttpAuthLoginV1Serve".
		fullPath := parts[1] + "." + suffix // rejoin: "http" + "auth.login.v1.serve"
		var b strings.Builder
		for _, seg := range strings.Split(fullPath, ".") {
			b.WriteString(kebabToCamelCase(seg))
		}
		return resolvedRef{
			Kind:       PrefixContract,
			RunPattern: b.String(),
		}, nil

	default:
		return resolvedRef{}, errcode.New(errcode.ErrCheckRefInvalid,
			fmt.Sprintf("ref %q has unknown prefix %q (expected journey, smoke, unit, or contract)", ref, prefix))
	}
}

// validateSegment rejects path segments that could cause directory traversal.
func validateSegment(s, field string) error {
	if s == "" || s == "." || strings.Contains(s, "..") || strings.ContainsAny(s, `/\`) {
		return errcode.New(errcode.ErrCheckRefInvalid,
			fmt.Sprintf("%s %q contains path traversal or separator", field, s))
	}
	return nil
}
