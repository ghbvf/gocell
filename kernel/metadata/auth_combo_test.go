package metadata

import "testing"

// TestAuthComboLegal_AgainstWhitelist enumerates all 32 combinations of the
// 5 auth bool fields and asserts each matches the explicit hand-maintained
// whitelist of legal combinations. The whitelist serves as an independent
// oracle: if AuthComboLegal regresses, divergence surfaces here. If the rules
// change, both AuthComboLegal and this whitelist must be updated together.
//
// Legal combinations (7 of 32):
//
//	"p-r-s-b-c"  default authenticated route (all fields false / omitted)
//	"P-r-s-b-c"  public only
//	"p-R-s-b-c"  passwordResetExempt only
//	"p-r-S-b-c"  serviceOwned only
//	"p-r-s-B-c"  bootstrap only
//	"p-r-s-b-C"  clientsOnly only
//	"p-R-S-b-c"  serviceOwned + passwordResetExempt
//
// All other 25 combinations must be rejected.
//
// INVARIANT: AUTH-SCHEMA-GOVERNANCE-BOOL-SEMANTICS-01.
func TestAuthComboLegal_AgainstWhitelist(t *testing.T) {
	legalNames := map[string]struct{}{
		"p-r-s-b-c": {},
		"P-r-s-b-c": {},
		"p-R-s-b-c": {},
		"p-r-S-b-c": {},
		"p-r-s-B-c": {},
		"p-r-s-b-C": {},
		"p-R-S-b-c": {},
	}

	count := 0
	IterateAuthBoolCombos(func(auth HTTPAuthMeta, name string) {
		count++
		t.Run(name, func(t *testing.T) {
			_, expectedLegal := legalNames[name]
			actual := AuthComboLegal(auth)
			if actual != expectedLegal {
				t.Errorf("AuthComboLegal(%+v) = %v, want %v",
					auth, actual, expectedLegal)
			}
		})
	})
	if count != 32 {
		t.Errorf("IterateAuthBoolCombos enumerated %d combinations, want 32 — "+
			"a bool field may have been added without doubling the matrix; "+
			"update the whitelist in this test alongside the helper", count)
	}
}

// TestIterateAuthBoolCombos_NamesUnique guards against bitmap encoding bugs by
// asserting every emitted name is unique across the 32-iteration window.
func TestIterateAuthBoolCombos_NamesUnique(t *testing.T) {
	seen := make(map[string]int)
	IterateAuthBoolCombos(func(_ HTTPAuthMeta, name string) {
		seen[name]++
	})
	for name, n := range seen {
		if n != 1 {
			t.Errorf("name %q emitted %d times, want exactly 1", name, n)
		}
	}
}
