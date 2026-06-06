// INVARIANT: ADAPTER-RETURNS-DECLARED-TYPES-01
//
// This _test.go only dogfoods + precision-gates the rule. The importable rule
// body (CheckAdapterReturnsDeclaredTypes + collectAdapterReturnsViolations +
// collectAdapterReturnsViolationsAt + collectAdapterFileViolations +
// loadContractStatusSets + importPathToContractID + gatherAdapterFiles +
// extractAdapterReturnStatuses + isAdapterMethod + returnTypeEndsInResponseObject +
// walkReturns + compositeLitTypeName + resolveContractImports + importAlias +
// contractIDForTypeName + extractMethodPrefix + sortedStatuses +
// adapterReturn type + responseStructPattern var + adapterReturnsDeclaredRule const,
// all module-path-agnostic via moduleImportPath) lives in the non-test companion
// adapter_returns_declared_types.go (M3 #1639) so it is fork-safe — single
// source, no parallel rule body. Registration in StandardCellRules (this rule is
// external-applicable) is deferred to #1706; see that .go's godoc for rationale.
package archtest

import (
	"path/filepath"
	"testing"
)

// TestAdapterReturnsDeclaredTypes dogfoods ADAPTER-RETURNS-DECLARED-TYPES-01
// against GoCell itself by calling the same CheckAdapterReturnsDeclaredTypes
// that holds the importable rule body — single source.
func TestAdapterReturnsDeclaredTypes(t *testing.T) {
	t.Parallel()
	Report(t, adapterReturnsDeclaredRule, CheckAdapterReturnsDeclaredTypes(t, ConfigForExternalCell{}))
}

// TestAdapterReturnsDeclaredTypes_GoodFixture verifies that the good/ testdata
// fixture passes the ADAPTER-RETURNS-DECLARED-TYPES-01 check (zero violations).
func TestAdapterReturnsDeclaredTypes_GoodFixture(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	goodRoot := filepath.Join(root, "tools", "archtest", "testdata", "adapter_returns", "good")

	modulePath, err := moduleImportPath(root)
	if err != nil {
		t.Fatalf("read module path: %v", err)
	}

	contractStatuses, err := loadContractStatusSets(goodRoot)
	if err != nil {
		t.Fatalf("load contract status sets from good fixture: %v", err)
	}

	files, err := gatherAdapterFiles(goodRoot)
	if err != nil {
		t.Fatalf("gather adapter files from good fixture: %v", err)
	}

	diags := collectAdapterReturnsViolationsAt(goodRoot, modulePath, files, contractStatuses)
	if len(diags) != 0 {
		t.Errorf("%s: good fixture must produce 0 violations; got %d", adapterReturnsDeclaredRule, len(diags))
	}
}

// TestAdapterReturnsDeclaredTypes_BadFixture verifies that the bad/ testdata
// fixture triggers at least one violation (self-validation of the scanner).
func TestAdapterReturnsDeclaredTypes_BadFixture(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	badRoot := filepath.Join(root, "tools", "archtest", "testdata", "adapter_returns", "bad")

	modulePath, err := moduleImportPath(root)
	if err != nil {
		t.Fatalf("read module path: %v", err)
	}

	contractStatuses, err := loadContractStatusSets(badRoot)
	if err != nil {
		t.Fatalf("load contract status sets from bad fixture: %v", err)
	}

	files, err := gatherAdapterFiles(badRoot)
	if err != nil {
		t.Fatalf("gather adapter files from bad fixture: %v", err)
	}

	diags := collectAdapterReturnsViolationsAt(badRoot, modulePath, files, contractStatuses)
	if len(diags) == 0 {
		t.Errorf("%s: bad fixture must produce ≥1 violations; got 0 — scanner is not detecting the violation",
			adapterReturnsDeclaredRule)
	}
}

// TestImportPathToContractID tests the importPathToContractID helper.
func TestImportPathToContractID(t *testing.T) {
	t.Parallel()
	const mod = PlatformModulePath
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "auth_login",
			in:   PlatformModulePath + "/generated/contracts/http/auth/login/v1",
			want: "http.auth.login.v1",
		},
		{
			name: "audit_list",
			in:   PlatformModulePath + "/generated/contracts/http/audit/list/v1",
			want: "http.audit.list.v1",
		},
		{
			name: "internalapi_segment_reversed",
			in:   PlatformModulePath + "/generated/contracts/http/internalapi/devicecommands/list/v1",
			want: "http.internal.devicecommands.list.v1",
		},
		{
			name: "non_generated",
			in:   PlatformModulePath + "/cells/accesscore/slices/sessionlogin",
			want: "",
		},
		{
			name: "empty",
			in:   "",
			want: "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := importPathToContractID(mod, tc.in)
			if got != tc.want {
				t.Errorf("importPathToContractID(%q) = %q; want %q", tc.in, got, tc.want)
			}
		})
	}
}
