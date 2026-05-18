package archtest

import (
	"fmt"

	"github.com/ghbvf/gocell/tools/archtest/internal/typeseval"
)

// warmProductionPackages pre-loads the production package set into the
// SharedResolver cache so that subsequent Test* calls hit the cache instead
// of paying the cold-load cost.  It is the implementation body of TestMain's
// warm-up step; the separation keeps testmain_test.go free of direct
// typeseval imports (which would trip PASS-FUNNEL-LOADPACKAGES-01).
func warmProductionPackages(modRoot, modulePath string) error {
	if _, err := typeseval.LoadProductionPackages(modRoot, modulePath, false, nil); err != nil {
		return fmt.Errorf("warmProductionPackages: %w", err)
	}
	return nil
}
