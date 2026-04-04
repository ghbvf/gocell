package governance

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// validateREF01 checks that slice.belongsToCell references an existing cell.
func (v *Validator) validateREF01() []ValidationResult {
	var results []ValidationResult
	for key, s := range v.project.Slices {
		if _, ok := v.project.Cells[s.BelongsToCell]; !ok {
			results = append(results, ValidationResult{
				Code:      "REF-01",
				Severity:  SeverityError,
				IssueType: IssueRefNotFound,
				File:      sliceFile(key),
				Field:     "belongsToCell",
				Message:   fmt.Sprintf("slice %q references non-existent cell %q", s.ID, s.BelongsToCell),
			})
		}
	}
	return results
}

// validateREF02 checks that slice.contractUsages[].contract references an existing contract.
func (v *Validator) validateREF02() []ValidationResult {
	var results []ValidationResult
	for key, s := range v.project.Slices {
		for i, cu := range s.ContractUsages {
			if _, ok := v.project.Contracts[cu.Contract]; !ok {
				results = append(results, ValidationResult{
					Code:      "REF-02",
					Severity:  SeverityError,
					IssueType: IssueRefNotFound,
					File:      sliceFile(key),
					Field:     fmt.Sprintf("contractUsages[%d].contract", i),
					Message:   fmt.Sprintf("slice %q references non-existent contract %q", s.ID, cu.Contract),
				})
			}
		}
	}
	return results
}

// validateREF03 checks that contract.ownerCell is a cell (not an external actor).
func (v *Validator) validateREF03() []ValidationResult {
	var results []ValidationResult
	for _, c := range v.project.Contracts {
		if _, ok := v.project.Cells[c.OwnerCell]; !ok {
			results = append(results, ValidationResult{
				Code:      "REF-03",
				Severity:  SeverityError,
				IssueType: IssueRefNotFound,
				File:      contractFile(c.ID),
				Field:     "ownerCell",
				Message:   fmt.Sprintf("contract %q ownerCell %q is not a known cell", c.ID, c.OwnerCell),
			})
		}
	}
	return results
}

// validateREF04 checks that cell.id equals the directory name.
// The Cells map is keyed by cell ID; the directory is derived from
// the cells/{cell-id}/cell.yaml convention.
func (v *Validator) validateREF04() []ValidationResult {
	var results []ValidationResult
	for key, c := range v.project.Cells {
		// The map key is the cell ID (set by parser from the YAML id field).
		// The directory name is also key since parser uses m.ID as the key.
		// We check that c.ID matches the map key.
		if c.ID != key {
			results = append(results, ValidationResult{
				Code:      "REF-04",
				Severity:  SeverityError,
				IssueType: IssueRefNotFound,
				File:      cellFile(key),
				Field:     "id",
				Message:   fmt.Sprintf("cell id %q does not match map key %q (expected directory name)", c.ID, key),
			})
		}
	}
	return results
}

// validateREF05 checks that slice.id equals the directory name.
// The Slices map key is "cellID/sliceID"; we extract the sliceID part.
func (v *Validator) validateREF05() []ValidationResult {
	var results []ValidationResult
	for key, s := range v.project.Slices {
		parts := strings.SplitN(key, "/", 2)
		if len(parts) != 2 {
			continue
		}
		expectedSliceID := parts[1]
		if s.ID != expectedSliceID {
			results = append(results, ValidationResult{
				Code:      "REF-05",
				Severity:  SeverityError,
				IssueType: IssueRefNotFound,
				File:      sliceFile(key),
				Field:     "id",
				Message:   fmt.Sprintf("slice id %q does not match directory name %q", s.ID, expectedSliceID),
			})
		}
	}
	return results
}

// validateREF06 checks that journey.cells[] references existing cells.
func (v *Validator) validateREF06() []ValidationResult {
	var results []ValidationResult
	for _, j := range v.project.Journeys {
		for i, cellRef := range j.Cells {
			if _, ok := v.project.Cells[cellRef]; !ok {
				results = append(results, ValidationResult{
					Code:      "REF-06",
					Severity:  SeverityError,
					IssueType: IssueRefNotFound,
					File:      journeyFile(j.ID),
					Field:     fmt.Sprintf("cells[%d]", i),
					Message:   fmt.Sprintf("journey %q references non-existent cell %q", j.ID, cellRef),
				})
			}
		}
	}
	return results
}

// validateREF07 checks that journey.contracts[] references existing contracts.
func (v *Validator) validateREF07() []ValidationResult {
	var results []ValidationResult
	for _, j := range v.project.Journeys {
		for i, cRef := range j.Contracts {
			if _, ok := v.project.Contracts[cRef]; !ok {
				results = append(results, ValidationResult{
					Code:      "REF-07",
					Severity:  SeverityError,
					IssueType: IssueRefNotFound,
					File:      journeyFile(j.ID),
					Field:     fmt.Sprintf("contracts[%d]", i),
					Message:   fmt.Sprintf("journey %q references non-existent contract %q", j.ID, cRef),
				})
			}
		}
	}
	return results
}

// validateREF08 checks that assembly.cells[] references existing cells.
func (v *Validator) validateREF08() []ValidationResult {
	var results []ValidationResult
	for _, a := range v.project.Assemblies {
		for i, cellRef := range a.Cells {
			if _, ok := v.project.Cells[cellRef]; !ok {
				results = append(results, ValidationResult{
					Code:      "REF-08",
					Severity:  SeverityError,
					IssueType: IssueRefNotFound,
					File:      assemblyFile(a.ID),
					Field:     fmt.Sprintf("cells[%d]", i),
					Message:   fmt.Sprintf("assembly %q references non-existent cell %q", a.ID, cellRef),
				})
			}
		}
	}
	return results
}

// validateREF09 checks that l0Dependencies[].cell references an existing cell.
func (v *Validator) validateREF09() []ValidationResult {
	var results []ValidationResult
	for _, c := range v.project.Cells {
		for i, dep := range c.L0Dependencies {
			if _, ok := v.project.Cells[dep.Cell]; !ok {
				results = append(results, ValidationResult{
					Code:      "REF-09",
					Severity:  SeverityError,
					IssueType: IssueRefNotFound,
					File:      cellFile(c.ID),
					Field:     fmt.Sprintf("l0Dependencies[%d].cell", i),
					Message:   fmt.Sprintf("cell %q l0Dependencies references non-existent cell %q", c.ID, dep.Cell),
				})
			}
		}
	}
	return results
}

// --- file path helpers ---

func cellFile(cellID string) string {
	return fmt.Sprintf("cells/%s/cell.yaml", cellID)
}

func sliceFile(key string) string {
	// key is "cellID/sliceID"
	parts := strings.SplitN(key, "/", 2)
	if len(parts) == 2 {
		return fmt.Sprintf("cells/%s/slices/%s/slice.yaml", parts[0], parts[1])
	}
	return key
}

func contractFile(contractID string) string {
	// contract IDs are like "http.auth.login.v1"
	// directory: contracts/http/auth/login/v1/contract.yaml
	segments := strings.Split(contractID, ".")
	return fmt.Sprintf("contracts/%s/contract.yaml", strings.Join(segments, "/"))
}

func journeyFile(journeyID string) string {
	return fmt.Sprintf("journeys/%s.yaml", journeyID)
}

func assemblyFile(assemblyID string) string {
	return fmt.Sprintf("assemblies/%s/assembly.yaml", assemblyID)
}

// validateREF10 checks that every assembly has a non-empty build.entrypoint.
func (v *Validator) validateREF10() []ValidationResult {
	var results []ValidationResult
	for _, a := range v.project.Assemblies {
		if a.Build.Entrypoint == "" {
			results = append(results, ValidationResult{
				Code:      "REF-10",
				Severity:  SeverityError,
				IssueType: IssueRequired,
				File:      assemblyFile(a.ID),
				Field:     "build.entrypoint",
				Message:   fmt.Sprintf("assembly %q must have build.entrypoint", a.ID),
			})
		}
	}
	return results
}

// isWithinRoot checks that target resolves to a path inside root.
func isWithinRoot(root, target string) bool {
	cleanRoot := filepath.Clean(root) + string(os.PathSeparator)
	cleanTarget := filepath.Clean(target)
	return strings.HasPrefix(cleanTarget, cleanRoot) || cleanTarget == filepath.Clean(root)
}

// validateREF11 checks that assembly.build.entrypoint file exists on disk.
// Skipped when root is empty.
func (v *Validator) validateREF11() []ValidationResult {
	if v.root == "" {
		return nil
	}
	var results []ValidationResult
	for _, a := range v.project.Assemblies {
		if a.Build.Entrypoint == "" {
			continue // REF-10 covers this
		}
		// The entrypoint path is relative to the repository root (parent of go.mod directory).
		repoRoot := repositoryRoot(v.root)
		fullPath := filepath.Join(repoRoot, a.Build.Entrypoint)
		if !isWithinRoot(repoRoot, fullPath) {
			results = append(results, ValidationResult{
				Code:      "REF-11",
				Severity:  SeverityError,
				IssueType: IssueInvalid,
				File:      assemblyFile(a.ID),
				Field:     "build.entrypoint",
				Message:   fmt.Sprintf("assembly %q build.entrypoint %q: path escapes project root", a.ID, a.Build.Entrypoint),
			})
			continue
		}
		if _, err := os.Stat(fullPath); err != nil {
			results = append(results, ValidationResult{
				Code:      "REF-11",
				Severity:  SeverityError,
				IssueType: IssueRefNotFound,
				File:      assemblyFile(a.ID),
				Field:     "build.entrypoint",
				Message:   fmt.Sprintf("assembly %q build.entrypoint %q does not exist", a.ID, a.Build.Entrypoint),
			})
		}
	}
	return results
}

// repositoryRoot returns the repository root from the project root.
// If root ends with "src", the repository root is the parent directory.
func repositoryRoot(root string) string {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return root
	}
	if filepath.Base(absRoot) == "src" {
		return filepath.Dir(absRoot)
	}
	return absRoot
}

// validateREF12 checks that contract.schemaRefs files exist on disk.
// Skipped when root is empty.
func (v *Validator) validateREF12() []ValidationResult {
	if v.root == "" {
		return nil
	}
	var results []ValidationResult
	for _, c := range v.project.Contracts {
		// Derive the contract directory from the contract ID.
		// Contract ID format: "http.auth.login.v1" -> "contracts/http/auth/login/v1/"
		contractDir := filepath.Join(v.root, contractDirFromID(c.ID))

		// Check each non-empty schemaRef field.
		type refEntry struct {
			field string
			value string
		}
		refs := []refEntry{
			{"schemaRefs.request", c.SchemaRefs.Request},
			{"schemaRefs.response", c.SchemaRefs.Response},
			{"schemaRefs.payload", c.SchemaRefs.Payload},
		}
		for _, ref := range refs {
			if ref.value == "" {
				continue
			}
			fullPath := filepath.Join(contractDir, ref.value)
			if !isWithinRoot(contractDir, fullPath) {
				results = append(results, ValidationResult{
					Code:      "REF-12",
					Severity:  SeverityError,
					IssueType: IssueInvalid,
					File:      contractFile(c.ID),
					Field:     ref.field,
					Message:   fmt.Sprintf("contract %q %s %q: path escapes project root", c.ID, ref.field, ref.value),
				})
				continue
			}
			if _, err := os.Stat(fullPath); err != nil {
				results = append(results, ValidationResult{
					Code:      "REF-12",
					Severity:  SeverityError,
					IssueType: IssueRefNotFound,
					File:      contractFile(c.ID),
					Field:     ref.field,
					Message:   fmt.Sprintf("contract %q %s points to missing file %q", c.ID, ref.field, ref.value),
				})
			}
		}
	}
	return results
}

// contractDirFromID converts a contract ID to its directory path.
// "http.auth.login.v1" -> "contracts/http/auth/login/v1"
func contractDirFromID(id string) string {
	segments := strings.Split(id, ".")
	return filepath.Join("contracts", filepath.Join(segments...))
}

// validateREF13 checks that the contract provider actor exists as a cell or actor.
func (v *Validator) validateREF13() []ValidationResult {
	var results []ValidationResult
	for _, c := range v.project.Contracts {
		provider := contractProvider(c)
		if provider == "" {
			continue // FMT-07 covers missing provider
		}
		if !v.actorExists(provider) {
			results = append(results, ValidationResult{
				Code:      "REF-13",
				Severity:  SeverityError,
				IssueType: IssueRefNotFound,
				File:      contractFile(c.ID),
				Field:     "endpoints",
				Message:   fmt.Sprintf("contract %q provider actor %q is not a known cell or actor", c.ID, provider),
			})
		}
	}
	return results
}

// validateREF14 checks that all contract consumer actors exist as cells or actors.
// The wildcard "*" is skipped.
func (v *Validator) validateREF14() []ValidationResult {
	var results []ValidationResult
	for _, c := range v.project.Contracts {
		consumers := contractConsumers(c)
		for i, actor := range consumers {
			if actor == "*" {
				continue
			}
			if !v.actorExists(actor) {
				results = append(results, ValidationResult{
					Code:      "REF-14",
					Severity:  SeverityError,
					IssueType: IssueRefNotFound,
					File:      contractFile(c.ID),
					Field:     fmt.Sprintf("endpoints.consumers[%d]", i),
					Message:   fmt.Sprintf("contract %q consumer actor %q is not a known cell or actor", c.ID, actor),
				})
			}
		}
	}
	return results
}

// validateREF15 checks that assembly.id matches the map key (directory name).
func (v *Validator) validateREF15() []ValidationResult {
	var results []ValidationResult
	for key, a := range v.project.Assemblies {
		if a.ID != key {
			results = append(results, ValidationResult{
				Code:      "REF-15",
				Severity:  SeverityError,
				IssueType: IssueMismatch,
				File:      assemblyFile(key),
				Field:     "id",
				Message:   fmt.Sprintf("assembly id %q does not match map key %q (expected directory name)", a.ID, key),
			})
		}
	}
	return results
}

// actorExists checks if an actor ID is a known cell or external actor.
func (v *Validator) actorExists(id string) bool {
	if _, ok := v.project.Cells[id]; ok {
		return true
	}
	for _, a := range v.project.Actors {
		if a.ID == id {
			return true
		}
	}
	return false
}
