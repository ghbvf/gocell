package metadata

// HTTPAuthMetaBoolFields is the count of bool fields on HTTPAuthMeta. It is the
// authoritative source of the matrix size used by IterateAuthBoolCombos
// (1 << HTTPAuthMetaBoolFields = 32 currently). TestHTTPAuthMetaFieldCount in
// auth_combo_test.go reflects on HTTPAuthMeta and fails CI when the actual
// bool field count drifts from this constant — adding a 6th bool field forces
// every consumer (IterateAuthBoolCombos, AuthComboLegal, the whitelist in
// TestAuthComboLegal_AgainstWhitelist) to be updated together.
const HTTPAuthMetaBoolFields = 5

// AuthComboMatrixSize is the size of the auth bool combination space
// (2 ** HTTPAuthMetaBoolFields). Tests iterate from 0 to this value.
const AuthComboMatrixSize = 1 << HTTPAuthMetaBoolFields

// AuthComboLegal returns true iff the bool combination on auth is permitted by
// FMT-27 semantics. This function is the single source of truth shared by:
//
//   - kernel/metadata/schemas/contract.schema.json (compile-time schema validation
//     via if/then const:true rules — see TestContractSchemaAuthBoolMatrix)
//   - kernel/governance/rules_fmt.go validateFMT27 (runtime governance check —
//     hasFMT27AuthModeConflict delegates to !AuthComboLegal)
//   - kernel/metadata/auth_combo_test.go (oracle whitelist test)
//
// Rules:
//   - Core modes {Public, PasswordResetExempt, Bootstrap, ClientsOnly}: at most
//     one may be true.
//   - ServiceOwned: may combine with PasswordResetExempt; mutually exclusive
//     with Public, Bootstrap, and ClientsOnly.
//
// INVARIANT: AUTH-SCHEMA-GOVERNANCE-BOOL-SEMANTICS-01
// ref: kubernetes/kubernetes pkg/securitycontext/util.go (capability mode mutex)
func AuthComboLegal(auth HTTPAuthMeta) bool {
	coreModes := 0
	if auth.Public {
		coreModes++
	}
	if auth.Bootstrap {
		coreModes++
	}
	if auth.PasswordResetExempt {
		coreModes++
	}
	if auth.ClientsOnly {
		coreModes++
	}
	if coreModes > 1 {
		return false
	}
	if auth.ServiceOwned && (auth.Public || auth.Bootstrap || auth.ClientsOnly) {
		return false
	}
	return true
}

// IterateAuthBoolCombos enumerates all 2 ** HTTPAuthMetaBoolFields (= 32 today)
// combinations of HTTPAuthMeta's bool fields. Named-field struct literals are
// used here for readability — Go does NOT report a compile error when a new
// field is added to HTTPAuthMeta, so the safety net is a separate reflect-based
// guard: TestHTTPAuthMetaFieldCount in auth_combo_test.go fails CI if the bool
// field count drifts from HTTPAuthMetaBoolFields, forcing this helper, the
// matrix size constant, AuthComboLegal, and the test whitelist to be updated
// together.
//
// HTTPAuthMeta.Responses ([]int) is intentionally excluded: it is not a
// mutex-governed flag and does not participate in FMT-27 semantics.
//
// name encodes each field as one character: uppercase = true, lowercase = false.
// Order P-R-S-B-C: Public / PasswordResetExempt (R) / ServiceOwned / Bootstrap /
// ClientsOnly. Example: "P-r-s-b-c" = Public:true, all others false.
func IterateAuthBoolCombos(fn func(auth HTTPAuthMeta, name string)) {
	for bits := 0; bits < AuthComboMatrixSize; bits++ {
		auth := HTTPAuthMeta{
			Public:              bits&0x01 != 0,
			PasswordResetExempt: bits&0x02 != 0,
			ServiceOwned:        bits&0x04 != 0,
			Bootstrap:           bits&0x08 != 0,
			ClientsOnly:         bits&0x10 != 0,
		}
		fn(auth, encodeAuthCombo(auth))
	}
}

func encodeAuthCombo(auth HTTPAuthMeta) string {
	letter := func(field bool, upper, lower byte) byte {
		if field {
			return upper
		}
		return lower
	}
	return string([]byte{
		letter(auth.Public, 'P', 'p'),
		'-',
		letter(auth.PasswordResetExempt, 'R', 'r'),
		'-',
		letter(auth.ServiceOwned, 'S', 's'),
		'-',
		letter(auth.Bootstrap, 'B', 'b'),
		'-',
		letter(auth.ClientsOnly, 'C', 'c'),
	})
}
