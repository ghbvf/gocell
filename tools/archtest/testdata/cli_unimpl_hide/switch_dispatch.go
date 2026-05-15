// Fixture for CLI-UNIMPL-HIDE-01 reverse self-check (upstream-Hard).
//
// A runGenerate that dispatches via a string switch and never calls
// findSub — the pre-PR shape that let `generate indexes` be visible yet
// unimplemented. scanDispatchSwitchFree MUST flag it (switch present +
// findSub absent). Not compiled (testdata).
package fixture

import "fmt"

func runGenerate(args []string) error {
	switch args[0] {
	case "assembly":
		return nil
	case "indexes":
		return fmt.Errorf("placeholder")
	default:
		return fmt.Errorf("unknown")
	}
}
