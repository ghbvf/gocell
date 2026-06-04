package archtest

import (
	"fmt"

	"github.com/ghbvf/gocell/tools/archtest/internal/typeseval"
	"github.com/ghbvf/gocell/tools/workspace"
)

// warmProductionPackages pre-loads the workspace production package set into the
// SharedResolver cache so that subsequent Test* calls hit the cache instead of
// paying the cold-load cost. It enumerates the workspace module set itself (from
// go.work) so TestMain stays free of direct typeseval / workspace-enumeration
// imports (which would trip PASS-FUNNEL-LOADPACKAGES-01).
func warmProductionPackages(modRoot string) error {
	modules, err := workspace.Modules(modRoot)
	if err != nil {
		return fmt.Errorf("warmProductionPackages: %w", err)
	}
	if _, err := typeseval.LoadProductionPackages(modRoot, modules, false, nil); err != nil {
		return fmt.Errorf("warmProductionPackages: %w", err)
	}
	return nil
}
