// INVARIANT: ADAPTER-RETURNS-DECLARED-TYPES-01
//
// This _test.go only dogfoods + precision-gates the rule. The importable rule
// body (CheckAdapterReturnsDeclaredTypes + collectAdapterReturnsViolations +
// collectAdapterReturnsViolationsAt + collectAdapterFileViolations +
// loadContractStatusSets + buildContractImportIndex + gatherAdapterFiles +
// extractAdapterReturnStatuses + isAdapterMethod + returnTypeEndsInResponseObject +
// walkReturns + compositeLitTypeName + resolveContractImports + importAlias +
// contractIDForTypeName + extractMethodPrefix + sortedStatuses +
// adapterReturn type + responseStructPattern var + adapterReturnsDeclaredRule const,
// all module-path-agnostic via moduleImportPath + contractpath single source) lives in the non-test companion
// adapter_returns_declared_types.go (M3 #1639) so it is fork-safe — single
// source, no parallel rule body. Registration in StandardCellRules (this rule is
// external-applicable) is deferred to #1706; see that .go's godoc for rationale.
package archtest

import (
	"path/filepath"
	"strings"
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
	// Precision oracle (codex #1708 F3): a ≥1 check goes green even if the
	// scanner degrades to a dummy diagnostic, the wrong line, the wrong status,
	// or the wrong contract. Pin to the exact known violation —
	// bad/cells/widgetcell/slices/widgetget/handler.go:16 returns
	// Get999JSONResponse, and http.widget.get.v1 declares only [200 404].
	if len(diags) != 1 {
		t.Fatalf("%s: bad fixture must produce exactly 1 violation; got %d: %+v",
			adapterReturnsDeclaredRule, len(diags), diags)
	}
	d := diags[0]
	if d.Rel == "" || !strings.HasSuffix(d.Rel, "cells/widgetcell/slices/widgetget/handler.go") {
		t.Errorf("%s: violation Rel = %q; want a handler.go-suffixed structured path", adapterReturnsDeclaredRule, d.Rel)
	}
	if d.Line != 16 {
		t.Errorf("%s: violation Line = %d; want 16 (return get.Get999JSONResponse)", adapterReturnsDeclaredRule, d.Line)
	}
	for _, want := range []string{"Get999JSONResponse", "status 999", "http.widget.get.v1", "[200 404]"} {
		if !strings.Contains(d.Message, want) {
			t.Errorf("%s: violation message %q must contain %q", adapterReturnsDeclaredRule, d.Message, want)
		}
	}
}

// TestBuildContractImportIndex verifies the forward import-path→contract-ID
// index that replaced the hand-written inverse (codex #1708 F4). The index is
// derived from contractpath.ContractIDToImportPath (single source), so this also
// confirms the "internal"→"internalapi" generated-segment rewrite round-trips
// and that only status-bearing contracts are indexed (non-generated / unknown
// import paths are absent → resolve to "").
func TestBuildContractImportIndex(t *testing.T) {
	t.Parallel()
	const mod = PlatformModulePath
	statuses := map[string]map[int]bool{
		"http.auth.login.v1":                   {200: true},
		"http.audit.list.v1":                   {200: true},
		"http.internal.devicecommands.list.v1": {200: true},
	}
	index := buildContractImportIndex(mod, statuses)

	cases := []struct {
		name string
		in   string
		want string // "" means the path must be absent from the index
	}{
		{
			name: "auth_login",
			in:   mod + "/generated/contracts/http/auth/login/v1",
			want: "http.auth.login.v1",
		},
		{
			name: "audit_list",
			in:   mod + "/generated/contracts/http/audit/list/v1",
			want: "http.audit.list.v1",
		},
		{
			name: "internalapi_segment_reversed",
			in:   mod + "/generated/contracts/http/internalapi/devicecommands/list/v1",
			want: "http.internal.devicecommands.list.v1",
		},
		{
			name: "non_generated",
			in:   mod + "/corecells/accesscore/slices/sessionlogin",
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
			if got := index[tc.in]; got != tc.want {
				t.Errorf("index[%q] = %q; want %q", tc.in, got, tc.want)
			}
		})
	}
}
