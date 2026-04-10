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
//	journey.{journeyID}.{suffix} → pkg="./journeys/...", pattern=CamelCase(suffix)
//	smoke.{cellID}.{suffix}     → pkg="./cells/{cellID}/...", pattern=CamelCase(suffix)
//	unit.{scope}.{suffix}       → pkg="" (caller provides), pattern=CamelCase(suffix)
//	contract.{id...}.{role}     → pkg="" (caller provides), pattern=CamelCase(role)
//
// Returns ErrCheckRefInvalid for unrecognized or malformed refs.
func resolveRef(ref string) (resolvedRef, error) {
	parts := strings.SplitN(ref, ".", 3)
	if len(parts) < 3 || parts[2] == "" {
		return resolvedRef{}, errcode.New(errcode.ErrCheckRefInvalid,
			fmt.Sprintf("ref %q must have at least 3 dot-separated segments", ref))
	}

	prefix := parts[0]
	suffix := parts[2] // everything after second dot

	switch prefix {
	case "journey":
		// journey.{journeyID}.{suffix}
		return resolvedRef{
			Kind:       "journey",
			Pkg:        "./journeys/...",
			RunPattern: kebabToCamelCase(suffix),
		}, nil

	case "smoke":
		// smoke.{cellID}.{suffix}
		cellID := parts[1]
		return resolvedRef{
			Kind:       "smoke",
			Pkg:        fmt.Sprintf("./cells/%s/...", cellID),
			RunPattern: kebabToCamelCase(suffix),
		}, nil

	case "unit":
		// unit.{scope}.{suffix} — caller provides package
		return resolvedRef{
			Kind:       "unit",
			RunPattern: kebabToCamelCase(suffix),
		}, nil

	case "contract":
		// contract.{contractID}.{role} — last dot-segment is the role
		// The suffix may contain dots (e.g., "http.auth.login.v1.serve"),
		// so extract the final segment as the role.
		lastDot := strings.LastIndexByte(suffix, '.')
		var role string
		if lastDot >= 0 {
			role = suffix[lastDot+1:]
		} else {
			role = suffix
		}
		return resolvedRef{
			Kind:       "contract",
			RunPattern: kebabToCamelCase(role),
		}, nil

	default:
		return resolvedRef{}, errcode.New(errcode.ErrCheckRefInvalid,
			fmt.Sprintf("ref %q has unknown prefix %q (expected journey, smoke, unit, or contract)", ref, prefix))
	}
}
