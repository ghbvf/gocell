package meta

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateRepositoryPassesMinimalValidRepo(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "actors.yaml"), `
- id: edge-bff
  type: external
  maxConsistencyLevel: L1
`)
	writeFile(t, filepath.Join(root, "cells", "access-core", "cell.yaml"), `
id: access-core
type: core
consistencyLevel: L2
schema:
  primary: cell_access_core
verify:
  smoke:
    - smoke.access-core.startup
l0Dependencies: []
`)
	writeFile(t, filepath.Join(root, "cells", "access-core", "slices", "session-login", "slice.yaml"), `
id: session-login
belongsToCell: access-core
contractUsages:
  - contract: http.auth.login.v1
    role: serve
verify:
  unit:
    - unit.session-login.service
  contract:
    - contract.http.auth.login.v1.serve
  waivers: []
`)
	writeFile(t, filepath.Join(root, "contracts", "http", "auth", "login", "v1", "contract.yaml"), `
id: http.auth.login.v1
kind: http
ownerCell: access-core
consistencyLevel: L1
lifecycle: active
endpoints:
  server: access-core
  clients:
    - edge-bff
schemaRefs:
  request: request.schema.json
  response: response.schema.json
`)
	writeFile(t, filepath.Join(root, "contracts", "http", "auth", "login", "v1", "request.schema.json"), `{}`)
	writeFile(t, filepath.Join(root, "contracts", "http", "auth", "login", "v1", "response.schema.json"), `{}`)
	writeFile(t, filepath.Join(root, "assemblies", "core-bundle", "assembly.yaml"), `
id: core-bundle
cells:
  - access-core
build:
  entrypoint: cmd/core-bundle/main.go
  binary: core-bundle
  deployTemplate: k8s
`)
	writeFile(t, filepath.Join(root, "cmd", "core-bundle", "main.go"), `package main`)
	writeFile(t, filepath.Join(root, "journeys", "J-login.yaml"), `
id: J-login
goal: login
cells:
  - access-core
contracts:
  - http.auth.login.v1
passCriteria:
  - text: ok
    mode: auto
    checkRef: journey.J-login.ok
`)
	writeFile(t, filepath.Join(root, "journeys", "status-board.yaml"), `
- journeyId: J-login
  state: doing
  risk: low
  blocker: ""
  updatedAt: 2026-04-04
`)

	result, err := ValidateRepository(root)
	if err != nil {
		t.Fatalf("ValidateRepository returned error: %v", err)
	}
	if result.HasErrors() {
		t.Fatalf("expected no errors, got %#v", result.Errors)
	}
}

func TestValidateRepositoryRejectsExpiredWaiver(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "cells", "access-core", "cell.yaml"), `
id: access-core
type: core
consistencyLevel: L2
schema:
  primary: cell_access_core
verify:
  smoke:
    - smoke.access-core.startup
l0Dependencies: []
`)
	writeFile(t, filepath.Join(root, "contracts", "http", "auth", "login", "v1", "contract.yaml"), `
id: http.auth.login.v1
kind: http
ownerCell: access-core
consistencyLevel: L1
lifecycle: active
endpoints:
  server: access-core
  clients:
    - edge-bff
`)
	writeFile(t, filepath.Join(root, "cells", "access-core", "slices", "session-login", "slice.yaml"), `
id: session-login
belongsToCell: access-core
contractUsages:
  - contract: http.auth.login.v1
    role: serve
verify:
  unit:
    - unit.session-login.service
  contract: []
  waivers:
    - contract: http.auth.login.v1
      owner: platform-team
      reason: temp
      expiresAt: 2000-01-01
`)

	result, err := ValidateRepository(root)
	if err != nil {
		t.Fatalf("ValidateRepository returned error: %v", err)
	}
	if !result.HasErrors() {
		t.Fatal("expected expired waiver to fail validation")
	}
	found := false
	for _, issue := range result.Errors {
		if strings.Contains(issue.Message, "expired") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected expired waiver error, got %#v", result.Errors)
	}
}

// ---------------------------------------------------------------------------
// ValidationResult methods
// ---------------------------------------------------------------------------

func TestValidationResultAddErrorAndWarning(t *testing.T) {
	r := &ValidationResult{Root: "/repo"}
	r.AddError("/repo/cells/x/cell.yaml", "missing %s", "id")
	r.AddWarning("/repo/contracts/y.yaml", "unused field %s", "extra")

	if len(r.Errors) != 1 {
		t.Fatalf("expected 1 error, got %d", len(r.Errors))
	}
	if len(r.Warnings) != 1 {
		t.Fatalf("expected 1 warning, got %d", len(r.Warnings))
	}
	if r.Errors[0].Level != "error" {
		t.Errorf("expected level=error, got %q", r.Errors[0].Level)
	}
	if r.Warnings[0].Level != "warning" {
		t.Errorf("expected level=warning, got %q", r.Warnings[0].Level)
	}
	if !strings.Contains(r.Errors[0].Message, "missing id") {
		t.Errorf("unexpected error message: %q", r.Errors[0].Message)
	}
}

func TestValidationResultHasErrors(t *testing.T) {
	r := &ValidationResult{Root: "/repo"}
	if r.HasErrors() {
		t.Fatal("expected no errors initially")
	}
	r.AddError("/path", "boom")
	if !r.HasErrors() {
		t.Fatal("expected HasErrors after AddError")
	}
}

func TestValidationResultPrintNoErrors(t *testing.T) {
	r := &ValidationResult{Root: "/repo"}
	var buf strings.Builder
	r.Print(&buf)
	out := buf.String()
	if !strings.Contains(out, "OK") {
		t.Errorf("expected OK in output, got %q", out)
	}
}

func TestValidationResultPrintNoErrorsWithWarnings(t *testing.T) {
	r := &ValidationResult{Root: "/repo"}
	r.AddWarning("/path", "something")
	var buf strings.Builder
	r.Print(&buf)
	out := buf.String()
	if !strings.Contains(out, "OK") {
		t.Errorf("expected OK in output, got %q", out)
	}
	if !strings.Contains(out, "warning") {
		t.Errorf("expected warning mention, got %q", out)
	}
}

func TestValidationResultPrintWithErrors(t *testing.T) {
	r := &ValidationResult{Root: "/repo"}
	r.AddError("/repo/x.yaml", "broken")
	r.AddWarning("/repo/y.yaml", "minor")
	var buf strings.Builder
	r.Print(&buf)
	out := buf.String()
	if !strings.Contains(out, "ERROR") {
		t.Errorf("expected ERROR in output, got %q", out)
	}
	if !strings.Contains(out, "FAIL") {
		t.Errorf("expected FAIL in output, got %q", out)
	}
	if !strings.Contains(out, "warning") {
		t.Errorf("expected warning mention, got %q", out)
	}
}

func TestValidationResultPrintErrorsWithoutWarnings(t *testing.T) {
	r := &ValidationResult{Root: "/repo"}
	r.AddError("/repo/x.yaml", "broken")
	var buf strings.Builder
	r.Print(&buf)
	out := buf.String()
	if !strings.Contains(out, "FAIL") {
		t.Errorf("expected FAIL in output, got %q", out)
	}
	if strings.Contains(out, "warning") {
		t.Errorf("expected no warning mention, got %q", out)
	}
}

// ---------------------------------------------------------------------------
// Validation: empty cell ID
// ---------------------------------------------------------------------------

func TestValidateRepositoryRejectsEmptyCellID(t *testing.T) {
	root := t.TempDir()
	ensureMinimalDirs(t, root)
	writeFile(t, filepath.Join(root, "cells", "bad-cell", "cell.yaml"), `
id: ""
type: core
consistencyLevel: L2
schema:
  primary: test
verify:
  smoke:
    - smoke.test
`)
	result, err := ValidateRepository(root)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.HasErrors() {
		t.Fatal("expected errors for empty cell ID")
	}
}

// ---------------------------------------------------------------------------
// Validation: cell ID mismatch with directory
// ---------------------------------------------------------------------------

func TestValidateRepositoryRejectsCellIDMismatch(t *testing.T) {
	root := t.TempDir()
	ensureMinimalDirs(t, root)
	writeFile(t, filepath.Join(root, "cells", "dir-name", "cell.yaml"), `
id: different-name
type: core
consistencyLevel: L2
schema:
  primary: test
verify:
  smoke:
    - smoke.test
`)
	result, err := ValidateRepository(root)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	found := false
	for _, issue := range result.Errors {
		if strings.Contains(issue.Message, "must match directory") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected cell ID mismatch error, got %#v", result.Errors)
	}
}

// ---------------------------------------------------------------------------
// Validation: non-L0 cell without schema.primary
// ---------------------------------------------------------------------------

func TestValidateRepositoryRejectsNonL0WithoutSchema(t *testing.T) {
	root := t.TempDir()
	ensureMinimalDirs(t, root)
	writeFile(t, filepath.Join(root, "cells", "no-schema", "cell.yaml"), `
id: no-schema
type: core
consistencyLevel: L2
verify:
  smoke:
    - smoke.test
`)
	result, err := ValidateRepository(root)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	found := false
	for _, issue := range result.Errors {
		if strings.Contains(issue.Message, "schema.primary is required") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected schema.primary error, got %#v", result.Errors)
	}
}

// ---------------------------------------------------------------------------
// Validation: assembly with unknown cell reference
// ---------------------------------------------------------------------------

func TestValidateRepositoryRejectsAssemblyUnknownCell(t *testing.T) {
	root := t.TempDir()
	ensureMinimalDirs(t, root)
	writeFile(t, filepath.Join(root, "assemblies", "bad-asm", "assembly.yaml"), `
id: bad-asm
cells:
  - nonexistent-cell
build:
  entrypoint: cmd/main.go
  binary: test
`)
	writeFile(t, filepath.Join(root, "cmd", "main.go"), `package main`)
	result, err := ValidateRepository(root)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	found := false
	for _, issue := range result.Errors {
		if strings.Contains(issue.Message, "unknown cell") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected unknown cell error, got %#v", result.Errors)
	}
}

// ---------------------------------------------------------------------------
// Validation: assembly with missing entrypoint
// ---------------------------------------------------------------------------

func TestValidateRepositoryRejectsAssemblyMissingEntrypoint(t *testing.T) {
	root := t.TempDir()
	ensureMinimalDirs(t, root)
	writeFile(t, filepath.Join(root, "assemblies", "no-entry", "assembly.yaml"), `
id: no-entry
cells: []
build:
  entrypoint: ""
  binary: test
`)
	result, err := ValidateRepository(root)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	found := false
	for _, issue := range result.Errors {
		if strings.Contains(issue.Message, "entrypoint is required") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected entrypoint error, got %#v", result.Errors)
	}
}

// ---------------------------------------------------------------------------
// Validation: assembly with non-existent entrypoint file
// ---------------------------------------------------------------------------

func TestValidateRepositoryRejectsAssemblyEntrypointNotExist(t *testing.T) {
	root := t.TempDir()
	ensureMinimalDirs(t, root)
	writeFile(t, filepath.Join(root, "assemblies", "bad-entry", "assembly.yaml"), `
id: bad-entry
cells: []
build:
  entrypoint: cmd/missing/main.go
  binary: test
`)
	result, err := ValidateRepository(root)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	found := false
	for _, issue := range result.Errors {
		if strings.Contains(issue.Message, "does not exist") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected entrypoint does-not-exist error, got %#v", result.Errors)
	}
}

// ---------------------------------------------------------------------------
// Validation: assembly ID mismatch with directory
// ---------------------------------------------------------------------------

func TestValidateRepositoryRejectsAssemblyIDMismatch(t *testing.T) {
	root := t.TempDir()
	ensureMinimalDirs(t, root)
	writeFile(t, filepath.Join(root, "assemblies", "dir-name", "assembly.yaml"), `
id: different-name
cells: []
build:
  entrypoint: cmd/main.go
  binary: test
`)
	writeFile(t, filepath.Join(root, "cmd", "main.go"), `package main`)
	result, err := ValidateRepository(root)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	found := false
	for _, issue := range result.Errors {
		if strings.Contains(issue.Message, "must match directory") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected assembly ID mismatch error, got %#v", result.Errors)
	}
}

// ---------------------------------------------------------------------------
// Validation: empty assembly ID
// ---------------------------------------------------------------------------

func TestValidateRepositoryRejectsEmptyAssemblyID(t *testing.T) {
	root := t.TempDir()
	ensureMinimalDirs(t, root)
	writeFile(t, filepath.Join(root, "assemblies", "no-id", "assembly.yaml"), `
id: ""
cells: []
build:
  entrypoint: cmd/main.go
  binary: test
`)
	writeFile(t, filepath.Join(root, "cmd", "main.go"), `package main`)
	result, err := ValidateRepository(root)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	found := false
	for _, issue := range result.Errors {
		if strings.Contains(issue.Message, "assembly.id is required") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected assembly id required error, got %#v", result.Errors)
	}
}

// ---------------------------------------------------------------------------
// Validation: contract with missing ownerCell
// ---------------------------------------------------------------------------

func TestValidateRepositoryRejectsContractMissingOwner(t *testing.T) {
	root := t.TempDir()
	ensureMinimalDirs(t, root)
	writeFile(t, filepath.Join(root, "contracts", "http", "test", "v1", "contract.yaml"), `
id: http.test.v1
kind: http
ownerCell: ""
consistencyLevel: L1
lifecycle: active
endpoints:
  server: ""
  clients: []
`)
	result, err := ValidateRepository(root)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.HasErrors() {
		t.Fatal("expected errors for missing owner")
	}
}

// ---------------------------------------------------------------------------
// Validation: contract with empty ID
// ---------------------------------------------------------------------------

func TestValidateRepositoryRejectsEmptyContractID(t *testing.T) {
	root := t.TempDir()
	ensureMinimalDirs(t, root)
	writeFile(t, filepath.Join(root, "contracts", "http", "test", "v1", "contract.yaml"), `
id: ""
kind: http
ownerCell: access-core
`)
	result, err := ValidateRepository(root)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	found := false
	for _, issue := range result.Errors {
		if strings.Contains(issue.Message, "contract.id is required") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected contract id required error, got %#v", result.Errors)
	}
}

// ---------------------------------------------------------------------------
// Validation: slice with empty ID
// ---------------------------------------------------------------------------

func TestValidateRepositoryRejectsEmptySliceID(t *testing.T) {
	root := t.TempDir()
	ensureMinimalDirs(t, root)
	writeFile(t, filepath.Join(root, "cells", "access-core", "cell.yaml"), `
id: access-core
type: core
consistencyLevel: L0
verify:
  smoke:
    - smoke.test
`)
	writeFile(t, filepath.Join(root, "cells", "access-core", "slices", "bad-slice", "slice.yaml"), `
id: ""
belongsToCell: access-core
contractUsages: []
verify:
  unit: []
  contract: []
`)
	result, err := ValidateRepository(root)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	found := false
	for _, issue := range result.Errors {
		if strings.Contains(issue.Message, "slice.id is required") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected slice id required error, got %#v", result.Errors)
	}
}

// ---------------------------------------------------------------------------
// Validation: slice ID mismatch with directory
// ---------------------------------------------------------------------------

func TestValidateRepositoryRejectsSliceIDMismatch(t *testing.T) {
	root := t.TempDir()
	ensureMinimalDirs(t, root)
	writeFile(t, filepath.Join(root, "cells", "access-core", "cell.yaml"), `
id: access-core
type: core
consistencyLevel: L0
verify:
  smoke:
    - smoke.test
`)
	writeFile(t, filepath.Join(root, "cells", "access-core", "slices", "dir-name", "slice.yaml"), `
id: different-name
belongsToCell: access-core
contractUsages: []
verify:
  unit: []
  contract: []
`)
	result, err := ValidateRepository(root)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	found := false
	for _, issue := range result.Errors {
		if strings.Contains(issue.Message, "must match directory") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected slice ID mismatch error, got %#v", result.Errors)
	}
}

// ---------------------------------------------------------------------------
// Validation: slice belongsToCell mismatch
// ---------------------------------------------------------------------------

func TestValidateRepositoryRejectsSliceBelongsToCellMismatch(t *testing.T) {
	root := t.TempDir()
	ensureMinimalDirs(t, root)
	writeFile(t, filepath.Join(root, "cells", "access-core", "cell.yaml"), `
id: access-core
type: core
consistencyLevel: L0
verify:
  smoke:
    - smoke.test
`)
	writeFile(t, filepath.Join(root, "cells", "access-core", "slices", "my-slice", "slice.yaml"), `
id: my-slice
belongsToCell: wrong-cell
contractUsages: []
verify:
  unit: []
  contract: []
`)
	result, err := ValidateRepository(root)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	found := false
	for _, issue := range result.Errors {
		if strings.Contains(issue.Message, "must match parent cell") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected belongsToCell mismatch error, got %#v", result.Errors)
	}
}

// ---------------------------------------------------------------------------
// Validation: journey without status-board entry (warning)
// ---------------------------------------------------------------------------

func TestValidateRepositoryWarnsJourneyMissingStatusEntry(t *testing.T) {
	root := t.TempDir()
	ensureMinimalDirs(t, root)
	writeFile(t, filepath.Join(root, "journeys", "J-test.yaml"), `
id: J-test
goal: test
cells: []
contracts: []
passCriteria: []
`)
	writeFile(t, filepath.Join(root, "journeys", "status-board.yaml"), `
- journeyId: J-other
  state: doing
  risk: low
  blocker: ""
  updatedAt: 2026-04-04
`)
	result, err := ValidateRepository(root)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	found := false
	for _, issue := range result.Warnings {
		if strings.Contains(issue.Message, "no entry for journey") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected warning about missing status entry, got warnings=%#v errors=%#v", result.Warnings, result.Errors)
	}
}

// ---------------------------------------------------------------------------
// Validation: status-board references unknown journey (warning)
// ---------------------------------------------------------------------------

func TestValidateRepositoryWarnsStatusBoardUnknownJourney(t *testing.T) {
	root := t.TempDir()
	ensureMinimalDirs(t, root)
	writeFile(t, filepath.Join(root, "journeys", "status-board.yaml"), `
- journeyId: J-nonexistent
  state: doing
  risk: low
  blocker: ""
  updatedAt: 2026-04-04
`)
	result, err := ValidateRepository(root)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	found := false
	for _, issue := range result.Warnings {
		if strings.Contains(issue.Message, "unknown journey") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected warning about unknown journey in status-board, got %#v", result.Warnings)
	}
}

// ---------------------------------------------------------------------------
// Validation: waiver with missing fields
// ---------------------------------------------------------------------------

func TestValidateRepositoryRejectsIncompleteWaiver(t *testing.T) {
	root := t.TempDir()
	ensureMinimalDirs(t, root)
	writeFile(t, filepath.Join(root, "cells", "access-core", "cell.yaml"), `
id: access-core
type: core
consistencyLevel: L2
schema:
  primary: cell_access_core
verify:
  smoke:
    - smoke.test
`)
	writeFile(t, filepath.Join(root, "contracts", "http", "auth", "login", "v1", "contract.yaml"), `
id: http.auth.login.v1
kind: http
ownerCell: access-core
consistencyLevel: L1
lifecycle: active
endpoints:
  server: access-core
  clients:
    - edge-bff
`)
	writeFile(t, filepath.Join(root, "actors.yaml"), `
- id: edge-bff
  type: external
  maxConsistencyLevel: L1
`)
	writeFile(t, filepath.Join(root, "cells", "access-core", "slices", "session-login", "slice.yaml"), `
id: session-login
belongsToCell: access-core
contractUsages:
  - contract: http.auth.login.v1
    role: serve
verify:
  unit:
    - unit.session-login.service
  contract: []
  waivers:
    - contract: http.auth.login.v1
      owner: ""
      reason: ""
      expiresAt: ""
`)
	result, err := ValidateRepository(root)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.HasErrors() {
		t.Fatal("expected errors for incomplete waiver")
	}
	ownerMissing := false
	reasonMissing := false
	expiresMissing := false
	for _, issue := range result.Errors {
		if strings.Contains(issue.Message, "waiver.owner is required") {
			ownerMissing = true
		}
		if strings.Contains(issue.Message, "waiver.reason is required") {
			reasonMissing = true
		}
		if strings.Contains(issue.Message, "waiver.expiresAt is required") {
			expiresMissing = true
		}
	}
	if !ownerMissing {
		t.Error("expected waiver.owner required error")
	}
	if !reasonMissing {
		t.Error("expected waiver.reason required error")
	}
	if !expiresMissing {
		t.Error("expected waiver.expiresAt required error")
	}
}

// ---------------------------------------------------------------------------
// Validation: waiver with invalid date format
// ---------------------------------------------------------------------------

func TestValidateRepositoryRejectsWaiverInvalidDate(t *testing.T) {
	root := t.TempDir()
	ensureMinimalDirs(t, root)
	writeFile(t, filepath.Join(root, "cells", "access-core", "cell.yaml"), `
id: access-core
type: core
consistencyLevel: L2
schema:
  primary: cell_access_core
verify:
  smoke:
    - smoke.test
`)
	writeFile(t, filepath.Join(root, "contracts", "http", "auth", "login", "v1", "contract.yaml"), `
id: http.auth.login.v1
kind: http
ownerCell: access-core
consistencyLevel: L1
lifecycle: active
endpoints:
  server: access-core
  clients:
    - edge-bff
`)
	writeFile(t, filepath.Join(root, "actors.yaml"), `
- id: edge-bff
  type: external
  maxConsistencyLevel: L1
`)
	writeFile(t, filepath.Join(root, "cells", "access-core", "slices", "session-login", "slice.yaml"), `
id: session-login
belongsToCell: access-core
contractUsages:
  - contract: http.auth.login.v1
    role: serve
verify:
  unit:
    - unit.session-login.service
  contract: []
  waivers:
    - contract: http.auth.login.v1
      owner: platform-team
      reason: temp
      expiresAt: not-a-date
`)
	result, err := ValidateRepository(root)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	found := false
	for _, issue := range result.Errors {
		if strings.Contains(issue.Message, "must be YYYY-MM-DD") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected invalid date error, got %#v", result.Errors)
	}
}

// ---------------------------------------------------------------------------
// Validation: L0 dependency to unknown cell
// ---------------------------------------------------------------------------

func TestValidateRepositoryRejectsL0DepUnknownCell(t *testing.T) {
	root := t.TempDir()
	ensureMinimalDirs(t, root)
	writeFile(t, filepath.Join(root, "cells", "my-cell", "cell.yaml"), `
id: my-cell
type: core
consistencyLevel: L2
schema:
  primary: test
verify:
  smoke:
    - smoke.test
l0Dependencies:
  - cell: nonexistent-cell
    reason: test
`)
	writeFile(t, filepath.Join(root, "assemblies", "asm", "assembly.yaml"), `
id: asm
cells:
  - my-cell
build:
  entrypoint: cmd/main.go
  binary: test
`)
	writeFile(t, filepath.Join(root, "cmd", "main.go"), `package main`)
	result, err := ValidateRepository(root)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	found := false
	for _, issue := range result.Errors {
		if strings.Contains(issue.Message, "l0Dependency references unknown cell") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected l0 dep unknown cell error, got %#v", result.Errors)
	}
}

// ---------------------------------------------------------------------------
// Validation: L0 dependency target is not L0
// ---------------------------------------------------------------------------

func TestValidateRepositoryRejectsL0DepNonL0Target(t *testing.T) {
	root := t.TempDir()
	ensureMinimalDirs(t, root)
	writeFile(t, filepath.Join(root, "cells", "my-cell", "cell.yaml"), `
id: my-cell
type: core
consistencyLevel: L2
schema:
  primary: test
verify:
  smoke:
    - smoke.test
l0Dependencies:
  - cell: lib-cell
    reason: test
`)
	writeFile(t, filepath.Join(root, "cells", "lib-cell", "cell.yaml"), `
id: lib-cell
type: support
consistencyLevel: L1
schema:
  primary: lib
verify:
  smoke:
    - smoke.test
`)
	writeFile(t, filepath.Join(root, "assemblies", "asm", "assembly.yaml"), `
id: asm
cells:
  - my-cell
  - lib-cell
build:
  entrypoint: cmd/main.go
  binary: test
`)
	writeFile(t, filepath.Join(root, "cmd", "main.go"), `package main`)
	result, err := ValidateRepository(root)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	found := false
	for _, issue := range result.Errors {
		if strings.Contains(issue.Message, "must be an L0 cell") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected L0 target error, got %#v", result.Errors)
	}
}

// ---------------------------------------------------------------------------
// Validation: L0 dependency not in same assembly
// ---------------------------------------------------------------------------

func TestValidateRepositoryRejectsL0DepDifferentAssembly(t *testing.T) {
	root := t.TempDir()
	ensureMinimalDirs(t, root)
	writeFile(t, filepath.Join(root, "cells", "my-cell", "cell.yaml"), `
id: my-cell
type: core
consistencyLevel: L2
schema:
  primary: test
verify:
  smoke:
    - smoke.test
l0Dependencies:
  - cell: lib-cell
    reason: test
`)
	writeFile(t, filepath.Join(root, "cells", "lib-cell", "cell.yaml"), `
id: lib-cell
type: support
consistencyLevel: L0
verify:
  smoke:
    - smoke.test
`)
	// Different assemblies
	writeFile(t, filepath.Join(root, "assemblies", "asm1", "assembly.yaml"), `
id: asm1
cells:
  - my-cell
build:
  entrypoint: cmd/main.go
  binary: test
`)
	writeFile(t, filepath.Join(root, "assemblies", "asm2", "assembly.yaml"), `
id: asm2
cells:
  - lib-cell
build:
  entrypoint: cmd/main.go
  binary: test
`)
	writeFile(t, filepath.Join(root, "cmd", "main.go"), `package main`)
	result, err := ValidateRepository(root)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	found := false
	for _, issue := range result.Errors {
		if strings.Contains(issue.Message, "must be in the same assembly") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected same-assembly error, got %#v", result.Errors)
	}
}

// ---------------------------------------------------------------------------
// Validation: contract kind mismatch with directory
// ---------------------------------------------------------------------------

func TestValidateRepositoryRejectsContractKindMismatch(t *testing.T) {
	root := t.TempDir()
	ensureMinimalDirs(t, root)
	writeFile(t, filepath.Join(root, "cells", "access-core", "cell.yaml"), `
id: access-core
type: core
consistencyLevel: L2
schema:
  primary: test
verify:
  smoke:
    - smoke.test
`)
	// Contract in "http" directory but kind is "event"
	writeFile(t, filepath.Join(root, "contracts", "http", "test", "v1", "contract.yaml"), `
id: http.test.v1
kind: event
ownerCell: access-core
consistencyLevel: L1
lifecycle: active
endpoints:
  publisher: access-core
  subscribers:
    - edge-bff
`)
	writeFile(t, filepath.Join(root, "actors.yaml"), `
- id: edge-bff
  type: external
  maxConsistencyLevel: L1
`)
	result, err := ValidateRepository(root)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	found := false
	for _, issue := range result.Errors {
		if strings.Contains(issue.Message, "must match directory kind") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected kind mismatch error, got %#v", result.Errors)
	}
}

// ---------------------------------------------------------------------------
// Validation: contract with missing schemaRef file
// ---------------------------------------------------------------------------

func TestValidateRepositoryRejectsContractMissingSchemaRefFile(t *testing.T) {
	root := t.TempDir()
	ensureMinimalDirs(t, root)
	writeFile(t, filepath.Join(root, "cells", "access-core", "cell.yaml"), `
id: access-core
type: core
consistencyLevel: L2
schema:
  primary: test
verify:
  smoke:
    - smoke.test
`)
	writeFile(t, filepath.Join(root, "contracts", "http", "test", "v1", "contract.yaml"), `
id: http.test.v1
kind: http
ownerCell: access-core
consistencyLevel: L1
lifecycle: active
endpoints:
  server: access-core
  clients:
    - "*"
schemaRefs:
  request: missing-file.json
`)
	result, err := ValidateRepository(root)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	found := false
	for _, issue := range result.Errors {
		if strings.Contains(issue.Message, "missing file") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected missing schema ref file error, got %#v", result.Errors)
	}
}

// ---------------------------------------------------------------------------
// Validation: contract with empty schemaRef value
// ---------------------------------------------------------------------------

func TestValidateRepositoryRejectsEmptySchemaRef(t *testing.T) {
	root := t.TempDir()
	ensureMinimalDirs(t, root)
	writeFile(t, filepath.Join(root, "cells", "access-core", "cell.yaml"), `
id: access-core
type: core
consistencyLevel: L2
schema:
  primary: test
verify:
  smoke:
    - smoke.test
`)
	writeFile(t, filepath.Join(root, "contracts", "http", "test", "v1", "contract.yaml"), `
id: http.test.v1
kind: http
ownerCell: access-core
consistencyLevel: L1
lifecycle: active
endpoints:
  server: access-core
  clients:
    - "*"
schemaRefs:
  request: ""
`)
	result, err := ValidateRepository(root)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	found := false
	for _, issue := range result.Errors {
		if strings.Contains(issue.Message, "must not be empty") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected empty schemaRef error, got %#v", result.Errors)
	}
}

// ---------------------------------------------------------------------------
// Validation: slice with unknown contract usage
// ---------------------------------------------------------------------------

func TestValidateRepositoryRejectsSliceUnknownContractUsage(t *testing.T) {
	root := t.TempDir()
	ensureMinimalDirs(t, root)
	writeFile(t, filepath.Join(root, "cells", "access-core", "cell.yaml"), `
id: access-core
type: core
consistencyLevel: L0
verify:
  smoke:
    - smoke.test
`)
	writeFile(t, filepath.Join(root, "cells", "access-core", "slices", "my-slice", "slice.yaml"), `
id: my-slice
belongsToCell: access-core
contractUsages:
  - contract: nonexistent.contract.v1
    role: serve
verify:
  unit: []
  contract:
    - contract.nonexistent.contract.v1.serve
`)
	result, err := ValidateRepository(root)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	found := false
	for _, issue := range result.Errors {
		if strings.Contains(issue.Message, "unknown contract") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected unknown contract error, got %#v", result.Errors)
	}
}

// ---------------------------------------------------------------------------
// Validation: journey with no status board at all
// ---------------------------------------------------------------------------

func TestValidateRepositoryWarnsJourneyNoStatusBoard(t *testing.T) {
	root := t.TempDir()
	ensureMinimalDirs(t, root)
	writeFile(t, filepath.Join(root, "journeys", "J-test.yaml"), `
id: J-test
goal: test
cells: []
contracts: []
passCriteria: []
`)
	// No status-board.yaml at all
	result, err := ValidateRepository(root)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	found := false
	for _, issue := range result.Warnings {
		if strings.Contains(issue.Message, "status-board.yaml is missing") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected status-board missing warning, got %#v", result.Warnings)
	}
}

// ---------------------------------------------------------------------------
// Validation: empty repository (no errors)
// ---------------------------------------------------------------------------

func TestValidateRepositoryEmptyRepoNoErrors(t *testing.T) {
	root := t.TempDir()
	ensureMinimalDirs(t, root)
	result, err := ValidateRepository(root)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.HasErrors() {
		t.Fatalf("expected no errors for empty repo, got %#v", result.Errors)
	}
}

// ---------------------------------------------------------------------------
// Type methods
// ---------------------------------------------------------------------------

func TestCellFileEffectiveID(t *testing.T) {
	tests := []struct {
		name string
		file CellFile
		want string
	}{
		{"with id", CellFile{DirID: "dir", Cell: Cell{ID: "explicit"}}, "explicit"},
		{"without id", CellFile{DirID: "dir", Cell: Cell{ID: ""}}, "dir"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.file.EffectiveID(); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSliceFileEffectiveID(t *testing.T) {
	tests := []struct {
		name string
		file SliceFile
		want string
	}{
		{"with id", SliceFile{DirID: "dir", Slice: Slice{ID: "explicit"}}, "explicit"},
		{"without id", SliceFile{DirID: "dir", Slice: Slice{ID: ""}}, "dir"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.file.EffectiveID(); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSliceFileEffectiveBelongsToCell(t *testing.T) {
	tests := []struct {
		name string
		file SliceFile
		want string
	}{
		{"with belongsToCell", SliceFile{ParentCellDir: "dir", Slice: Slice{BelongsToCell: "explicit"}}, "explicit"},
		{"without belongsToCell", SliceFile{ParentCellDir: "dir", Slice: Slice{BelongsToCell: ""}}, "dir"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.file.EffectiveBelongsToCell(); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestContractFileEffectiveKind(t *testing.T) {
	tests := []struct {
		name string
		file ContractFile
		want string
	}{
		{"explicit kind", ContractFile{Contract: Contract{ID: "x", Kind: "event"}}, "event"},
		{"kind from ID", ContractFile{Contract: Contract{ID: "http.auth.v1", Kind: ""}}, "http"},
		{"empty ID no kind", ContractFile{Contract: Contract{ID: "", Kind: ""}}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.file.EffectiveKind(); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestContractFileProviderActor(t *testing.T) {
	tests := []struct {
		name string
		file ContractFile
		want string
	}{
		{"http server", ContractFile{Contract: Contract{Kind: "http", Endpoints: ContractEndpoints{Server: "cell-a"}}}, "cell-a"},
		{"event publisher", ContractFile{Contract: Contract{Kind: "event", Endpoints: ContractEndpoints{Publisher: "cell-b"}}}, "cell-b"},
		{"command handler", ContractFile{Contract: Contract{Kind: "command", Endpoints: ContractEndpoints{Handler: "cell-c"}}}, "cell-c"},
		{"projection provider", ContractFile{Contract: Contract{Kind: "projection", Endpoints: ContractEndpoints{Provider: "cell-d"}}}, "cell-d"},
		{"unknown kind", ContractFile{Contract: Contract{Kind: "unknown"}}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.file.ProviderActor(); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestContractFileConsumerActors(t *testing.T) {
	tests := []struct {
		name string
		file ContractFile
		want []string
	}{
		{"http clients", ContractFile{Contract: Contract{Kind: "http", Endpoints: ContractEndpoints{Clients: []string{"a", "b"}}}}, []string{"a", "b"}},
		{"event subscribers", ContractFile{Contract: Contract{Kind: "event", Endpoints: ContractEndpoints{Subscribers: []string{"c"}}}}, []string{"c"}},
		{"command invokers", ContractFile{Contract: Contract{Kind: "command", Endpoints: ContractEndpoints{Invokers: []string{"d"}}}}, []string{"d"}},
		{"projection readers", ContractFile{Contract: Contract{Kind: "projection", Endpoints: ContractEndpoints{Readers: []string{"e"}}}}, []string{"e"}},
		{"unknown kind", ContractFile{Contract: Contract{Kind: "unknown"}}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.file.ConsumerActors()
			if len(got) == 0 && len(tt.want) == 0 {
				return
			}
			if len(got) != len(tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("index %d: got %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestContractFileEffectiveOwnerCell(t *testing.T) {
	tests := []struct {
		name string
		file ContractFile
		want string
	}{
		{"explicit owner", ContractFile{Contract: Contract{OwnerCell: "cell-a", Kind: "http", Endpoints: ContractEndpoints{Server: "cell-b"}}}, "cell-a"},
		{"fallback to provider", ContractFile{Contract: Contract{OwnerCell: "", Kind: "http", Endpoints: ContractEndpoints{Server: "cell-b"}}}, "cell-b"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.file.EffectiveOwnerCell(); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Helper: relPath
// ---------------------------------------------------------------------------

func TestRelPath(t *testing.T) {
	got := relPath("/repo", "/repo/cells/x.yaml")
	if got != "cells/x.yaml" {
		t.Errorf("got %q, want %q", got, "cells/x.yaml")
	}
}

func TestRelPathFallback(t *testing.T) {
	// Save original and restore after test
	orig := filepathRel
	defer func() { filepathRel = orig }()

	filepathRel = func(_, _ string) (string, error) {
		return "", os.ErrNotExist
	}

	got := relPath("/repo", "/abs/path")
	if got != "/abs/path" {
		t.Errorf("expected fallback to absolute path, got %q", got)
	}
}

// ---------------------------------------------------------------------------
// Helper functions
// ---------------------------------------------------------------------------

func TestValidRoleForKind(t *testing.T) {
	tests := []struct {
		kind, role string
		want       bool
	}{
		{"http", "serve", true},
		{"http", "call", true},
		{"http", "publish", false},
		{"event", "publish", true},
		{"event", "subscribe", true},
		{"event", "serve", false},
		{"command", "handle", true},
		{"command", "invoke", true},
		{"command", "call", false},
		{"projection", "provide", true},
		{"projection", "read", true},
		{"projection", "serve", false},
		{"unknown", "serve", false},
	}
	for _, tt := range tests {
		t.Run(tt.kind+"/"+tt.role, func(t *testing.T) {
			if got := validRoleForKind(tt.kind, tt.role); got != tt.want {
				t.Errorf("validRoleForKind(%q, %q) = %v, want %v", tt.kind, tt.role, got, tt.want)
			}
		})
	}
}

func TestIsProviderRole(t *testing.T) {
	tests := []struct {
		kind, role string
		want       bool
	}{
		{"http", "serve", true},
		{"http", "call", false},
		{"event", "publish", true},
		{"event", "subscribe", false},
		{"command", "handle", true},
		{"command", "invoke", false},
		{"projection", "provide", true},
		{"projection", "read", false},
		{"unknown", "serve", false},
	}
	for _, tt := range tests {
		t.Run(tt.kind+"/"+tt.role, func(t *testing.T) {
			if got := isProviderRole(tt.kind, tt.role); got != tt.want {
				t.Errorf("isProviderRole(%q, %q) = %v, want %v", tt.kind, tt.role, got, tt.want)
			}
		})
	}
}

func TestActorListContains(t *testing.T) {
	tests := []struct {
		name   string
		actors []string
		want   string
		result bool
	}{
		{"exact match", []string{"a", "b"}, "b", true},
		{"wildcard", []string{"*"}, "anything", true},
		{"no match", []string{"a", "b"}, "c", false},
		{"empty list", nil, "a", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := actorListContains(tt.actors, tt.want); got != tt.result {
				t.Errorf("actorListContains(%v, %q) = %v, want %v", tt.actors, tt.want, got, tt.result)
			}
		})
	}
}

func TestSplitContractID(t *testing.T) {
	tests := []struct {
		input string
		want  int // expected number of parts
	}{
		{"", 0},
		{"http.auth.login.v1", 4},
		{"simple", 1},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			parts := splitContractID(tt.input)
			if parts == nil && tt.want == 0 {
				return
			}
			if len(parts) != tt.want {
				t.Errorf("splitContractID(%q) returned %d parts, want %d", tt.input, len(parts), tt.want)
			}
		})
	}
}

func TestRepositoryRoot(t *testing.T) {
	// When root ends with "src", should return parent
	got := repositoryRoot("/some/path/src")
	if !strings.HasSuffix(got, "path") {
		t.Errorf("expected parent of src, got %q", got)
	}

	// When root does not end with "src"
	got = repositoryRoot("/some/path/other")
	if !strings.HasSuffix(got, "other") {
		t.Errorf("expected same path, got %q", got)
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// ensureMinimalDirs creates the directories required by LoadRepository so that
// WalkDir does not fail on missing paths.
func ensureMinimalDirs(t *testing.T, root string) {
	t.Helper()
	for _, dir := range []string{"cells", "contracts", "assemblies", "journeys"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(strings.TrimLeft(content, "\n")), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
