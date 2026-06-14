// Package red_outside_ctor is a RED fixture for SCAFFOLD-DERIVED-FORCEOVERWRITE-01:
// a pathsafe.DerivedOverwrite call from a site other than
// tools/codegen/cellgen/stage_render.go::planDerivedArtifact must be flagged.
package red_outside_ctor

import "github.com/ghbvf/gocell/framework/pkg/pathsafe"

// rogueWrite calls the force-overwrite constructor from a non-sanctioned site.
func rogueWrite() pathsafe.PlannedFile {
	return pathsafe.DerivedOverwrite("/tmp/x", nil)
}
