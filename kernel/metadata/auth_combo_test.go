package metadata

import (
	"reflect"
	"testing"
)

// httpAuthMetaTotalFields is the expected total field count on HTTPAuthMeta:
// 5 bool (Public/PasswordResetExempt/ServiceOwned/Bootstrap/ClientsOnly) +
// 1 []int (Responses) = 6. Increment together with HTTPAuthMetaBoolFields when
// adding new fields and update IterateAuthBoolCombos / AuthComboLegal / the
// legalNames whitelist below in the same change.
const httpAuthMetaTotalFields = 6

// TestHTTPAuthMetaFieldCount is the static safeguard for IterateAuthBoolCombos.
// Go's named-field struct literals do not produce a compile error when fields
// are added to a struct, so this reflect-based assertion is the only signal
// catching a developer who adds a 6th auth bool but forgets to update
// IterateAuthBoolCombos / AuthComboLegal / HTTPAuthMetaBoolFields.
//
// When this test fails, the typical fix is:
//  1. include the new field in IterateAuthBoolCombos's struct literal,
//  2. bump HTTPAuthMetaBoolFields and httpAuthMetaTotalFields,
//  3. extend AuthComboLegal's rule chain,
//  4. extend the legalNames whitelist in TestAuthComboLegal_AgainstWhitelist
//     (the matrix space doubles automatically via AuthComboMatrixSize).
//
// INVARIANT: AUTH-SCHEMA-GOVERNANCE-BOOL-SEMANTICS-01.
func TestHTTPAuthMetaFieldCount(t *testing.T) {
	typ := reflect.TypeOf(HTTPAuthMeta{})

	gotTotal := typ.NumField()
	if gotTotal != httpAuthMetaTotalFields {
		t.Fatalf("HTTPAuthMeta has %d fields, want %d — a field was added or "+
			"removed; update IterateAuthBoolCombos, AuthComboLegal, "+
			"HTTPAuthMetaBoolFields, and the legalNames whitelist in lockstep",
			gotTotal, httpAuthMetaTotalFields)
	}

	gotBool := 0
	for i := 0; i < gotTotal; i++ {
		if typ.Field(i).Type.Kind() == reflect.Bool {
			gotBool++
		}
	}
	if gotBool != HTTPAuthMetaBoolFields {
		t.Fatalf("HTTPAuthMeta has %d bool fields, want HTTPAuthMetaBoolFields=%d "+
			"— matrix size mismatch; update IterateAuthBoolCombos and "+
			"HTTPAuthMetaBoolFields together (the matrix space is 2^N)",
			gotBool, HTTPAuthMetaBoolFields)
	}
}

// TestAuthComboLegal_AgainstWhitelist enforces parity between AuthComboLegal
// (algorithmic / intensional rule) and LegalAuthComboNames (hand-maintained /
// extensional rule). The whitelist serves as an independent oracle — if either
// statement regresses without the other, this test reports the divergence.
//
// INVARIANT: AUTH-SCHEMA-GOVERNANCE-BOOL-SEMANTICS-01.
func TestAuthComboLegal_AgainstWhitelist(t *testing.T) {
	count := 0
	IterateAuthBoolCombos(func(auth HTTPAuthMeta, name string) {
		count++
		t.Run(name, func(t *testing.T) {
			_, expectedLegal := LegalAuthComboNames[name]
			actual := AuthComboLegal(auth)
			if actual != expectedLegal {
				t.Errorf("AuthComboLegal(%+v) = %v, want %v",
					auth, actual, expectedLegal)
			}
		})
	})
	if count != AuthComboMatrixSize {
		t.Errorf("IterateAuthBoolCombos enumerated %d combinations, want %d — "+
			"a bool field may have been added without doubling the matrix; "+
			"update LegalAuthComboNames alongside the helper",
			count, AuthComboMatrixSize)
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
