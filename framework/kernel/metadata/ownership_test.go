package metadata

import "testing"

// TestOwnershipPathValid_Table verifies the DSL shape oracle for ownership path
// expressions. The predicate is the single source shared by governance FMT-32
// and schema/metadata validation.
//
// DSL: prefix must be ctx or path; followed by one or more dot-separated
// segments each starting with a lowercase letter (camelCase enforced).
func TestOwnershipPathValid_Table(t *testing.T) {
	cases := []struct {
		expr  string
		valid bool
	}{
		// valid
		{"ctx.subjectID", true},
		{"path.id.userID", true},
		{"ctx.a.b.c", true},
		{"path.id", true},
		// invalid: snake_case segment
		{"ctx.sub_ject", false},
		// invalid: single segment (no dot after prefix counts as prefix-only)
		{"path", false},
		// invalid: illegal prefix
		{"foo.bar", false},
		// invalid: empty string
		{"", false},
		// invalid: trailing empty segment (ends with dot)
		{"ctx.", false},
		// invalid: segment contains space
		{"ctx. subjectID", false},
		// invalid: prefix starts with uppercase
		{"Ctx.subjectID", false},
		// invalid: segment starts with uppercase (camelCase requires lowercase first char)
		{"ctx.SubjectID", false},
	}

	for _, tc := range cases {
		got := OwnershipPathValid(tc.expr)
		if got != tc.valid {
			t.Errorf("OwnershipPathValid(%q) = %v, want %v", tc.expr, got, tc.valid)
		}
	}
}

// TestOwnershipDeclarationRequired verifies the single-source predicate that
// determines whether a governance FMT-32 ownership block is required.
func TestOwnershipDeclarationRequired(t *testing.T) {
	cases := []struct {
		auth HTTPAuthMeta
		want bool
	}{
		{HTTPAuthMeta{ServiceOwned: true}, true},
		{HTTPAuthMeta{ServiceOwned: false}, false},
		{HTTPAuthMeta{ServiceOwned: true, PasswordResetExempt: true}, true},
	}

	for _, tc := range cases {
		got := OwnershipDeclarationRequired(tc.auth)
		if got != tc.want {
			t.Errorf("OwnershipDeclarationRequired(%+v) = %v, want %v", tc.auth, got, tc.want)
		}
	}
}
