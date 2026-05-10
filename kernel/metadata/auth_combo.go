package metadata

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
//   - Core modes {Public, Bootstrap, PasswordResetExempt, ClientsOnly}: at most
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

// IterateAuthBoolCombos enumerates all 32 (= 2^5) combinations of the 5 auth
// bool fields. Field literal is intentionally explicit — adding a new bool
// field to HTTPAuthMeta requires updating this helper (compile error surfaces
// the omission), then the matrix space doubles naturally and matrix tests
// surface unhandled cases.
//
// name encodes each field as one character: uppercase = true, lowercase = false.
// Order P-R-S-B-C: Public / passwordResetExempt (R) / ServiceOwned / Bootstrap /
// ClientsOnly. Example: "P-r-s-b-c" = Public:true, all others false.
func IterateAuthBoolCombos(fn func(auth HTTPAuthMeta, name string)) {
	for bits := 0; bits < 32; bits++ {
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
