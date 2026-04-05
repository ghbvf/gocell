package meta

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

type Issue struct {
	Level   string
	Path    string
	Message string
}

type ValidationResult struct {
	Root     string
	Errors   []Issue
	Warnings []Issue
}

func ValidateRepository(root string) (*ValidationResult, error) {
	repo, err := LoadRepository(root)
	if err != nil {
		return nil, err
	}

	result := &ValidationResult{Root: root}
	v := validator{
		repo:             repo,
		result:           result,
		cells:            map[string]*CellFile{},
		contracts:        map[string]*ContractFile{},
		assembliesByCell: map[string]string{},
	}
	v.index()
	v.validateCells()
	v.validateAssemblies()
	v.validateContracts()
	v.validateSlices()
	v.validateJourneys()
	v.validateStatusBoard()
	v.validateL0Dependencies()

	return result, nil
}

func (r *ValidationResult) HasErrors() bool {
	return len(r.Errors) > 0
}

func (r *ValidationResult) AddError(path, format string, args ...any) {
	r.Errors = append(r.Errors, Issue{
		Level:   "error",
		Path:    relPath(r.Root, path),
		Message: fmt.Sprintf(format, args...),
	})
}

func (r *ValidationResult) AddWarning(path, format string, args ...any) {
	r.Warnings = append(r.Warnings, Issue{
		Level:   "warning",
		Path:    relPath(r.Root, path),
		Message: fmt.Sprintf(format, args...),
	})
}

func (r *ValidationResult) Print(w io.Writer) {
	for _, issue := range r.Errors {
		fmt.Fprintf(w, "ERROR  %s: %s\n", issue.Path, issue.Message)
	}
	for _, issue := range r.Warnings {
		fmt.Fprintf(w, "WARN   %s: %s\n", issue.Path, issue.Message)
	}
	if len(r.Errors) == 0 {
		fmt.Fprintf(w, "OK     validate-meta passed")
		if len(r.Warnings) > 0 {
			fmt.Fprintf(w, " with %d warning(s)", len(r.Warnings))
		}
		fmt.Fprintln(w)
		return
	}
	fmt.Fprintf(w, "FAIL   validate-meta found %d error(s)", len(r.Errors))
	if len(r.Warnings) > 0 {
		fmt.Fprintf(w, " and %d warning(s)", len(r.Warnings))
	}
	fmt.Fprintln(w)
}

type validator struct {
	repo             *Repository
	result           *ValidationResult
	cells            map[string]*CellFile
	contracts        map[string]*ContractFile
	assembliesByCell map[string]string
}

func (v *validator) index() {
	for _, cell := range v.repo.Cells {
		v.cells[cell.EffectiveID()] = cell
	}
	for _, contract := range v.repo.Contracts {
		v.contracts[contract.Contract.ID] = contract
	}
	for _, assembly := range v.repo.Assemblies {
		for _, cellID := range assembly.Assembly.Cells {
			v.assembliesByCell[cellID] = assembly.Assembly.ID
		}
	}
}

func (v *validator) validateCells() {
	for _, cell := range v.repo.Cells {
		if cell.Cell.ID == "" {
			v.result.AddError(cell.Path, "cell.id is required")
		}
		if cell.Cell.ID != "" && cell.Cell.ID != cell.DirID {
			v.result.AddError(cell.Path, "cell.id %q must match directory %q", cell.Cell.ID, cell.DirID)
		}
		if cell.Cell.ConsistencyLevel != "L0" && cell.Cell.Schema.Primary == "" {
			v.result.AddError(cell.Path, "schema.primary is required for non-L0 cells")
		}
	}
}

func (v *validator) validateAssemblies() {
	for _, assembly := range v.repo.Assemblies {
		if assembly.Assembly.ID == "" {
			v.result.AddError(assembly.Path, "assembly.id is required")
		}
		if assembly.Assembly.ID != "" && assembly.Assembly.ID != assembly.DirID {
			v.result.AddError(assembly.Path, "assembly.id %q must match directory %q", assembly.Assembly.ID, assembly.DirID)
		}
		for _, cellID := range assembly.Assembly.Cells {
			if _, ok := v.cells[cellID]; !ok {
				v.result.AddError(assembly.Path, "assembly references unknown cell %q", cellID)
			}
		}
		if assembly.Assembly.Build.EntryPoint == "" {
			v.result.AddError(assembly.Path, "build.entrypoint is required")
		} else if !fileExists(filepath.Join(repositoryRoot(v.repo.Root), assembly.Assembly.Build.EntryPoint)) {
			v.result.AddError(assembly.Path, "build.entrypoint %q does not exist", assembly.Assembly.Build.EntryPoint)
		}
	}
}

func (v *validator) validateContracts() {
	for _, contract := range v.repo.Contracts {
		if contract.Contract.ID == "" {
			v.result.AddError(contract.Path, "contract.id is required")
			continue
		}

		if kind := contract.EffectiveKind(); kind != contract.KindDir {
			v.result.AddError(contract.Path, "contract kind %q must match directory kind %q", kind, contract.KindDir)
		}
		if owner := contract.EffectiveOwnerCell(); owner == "" {
			v.result.AddError(contract.Path, "ownerCell is required when provider is not a cell")
		} else if _, ok := v.cells[owner]; !ok {
			v.result.AddError(contract.Path, "ownerCell %q must reference an existing cell", owner)
		}

		provider := contract.ProviderActor()
		if provider == "" {
			v.result.AddError(contract.Path, "provider endpoint is required for kind %q", contract.EffectiveKind())
		} else if !v.actorExists(provider) {
			v.result.AddError(contract.Path, "provider actor %q does not exist", provider)
		}

		for _, actor := range contract.ConsumerActors() {
			if actor == "*" {
				continue
			}
			if !v.actorExists(actor) {
				v.result.AddError(contract.Path, "consumer actor %q does not exist", actor)
			}
		}

		for key, ref := range contract.Contract.SchemaRefs {
			if ref == "" {
				v.result.AddError(contract.Path, "schemaRefs.%s must not be empty", key)
				continue
			}
			schemaPath := filepath.Join(filepath.Dir(contract.Path), ref)
			if !fileExists(schemaPath) {
				v.result.AddError(contract.Path, "schemaRefs.%s points to missing file %q", key, ref)
			}
		}
	}
}

func (v *validator) validateSlices() {
	for _, slice := range v.repo.Slices {
		if slice.Slice.ID == "" {
			v.result.AddError(slice.Path, "slice.id is required")
		}
		if slice.Slice.ID != "" && slice.Slice.ID != slice.DirID {
			v.result.AddError(slice.Path, "slice.id %q must match directory %q", slice.Slice.ID, slice.DirID)
		}

		belongsToCell := slice.EffectiveBelongsToCell()
		if belongsToCell == "" {
			v.result.AddError(slice.Path, "belongsToCell is required or must be derivable from path")
			continue
		}
		if belongsToCell != slice.ParentCellDir {
			v.result.AddError(slice.Path, "belongsToCell %q must match parent cell directory %q", belongsToCell, slice.ParentCellDir)
		}
		if _, ok := v.cells[belongsToCell]; !ok {
			v.result.AddError(slice.Path, "belongsToCell %q must reference an existing cell", belongsToCell)
		}

		for _, usage := range slice.Slice.ContractUsages {
			contract, ok := v.contracts[usage.Contract]
			if !ok {
				v.result.AddError(slice.Path, "contractUsage references unknown contract %q", usage.Contract)
				continue
			}
			if !validRoleForKind(contract.EffectiveKind(), usage.Role) {
				v.result.AddError(slice.Path, "role %q is invalid for contract kind %q", usage.Role, contract.EffectiveKind())
			}
			if isProviderRole(contract.EffectiveKind(), usage.Role) {
				if contract.ProviderActor() != belongsToCell {
					v.result.AddError(slice.Path, "provider role %q for contract %q requires belongsToCell %q, got %q", usage.Role, usage.Contract, contract.ProviderActor(), belongsToCell)
				}
			} else if !actorListContains(contract.ConsumerActors(), belongsToCell) {
				v.result.AddError(slice.Path, "client role %q for contract %q requires belongsToCell %q to appear in contract consumers", usage.Role, usage.Contract, belongsToCell)
			}

			expectedVerify := fmt.Sprintf("contract.%s.%s", usage.Contract, usage.Role)
			hasVerify := slices.Contains(slice.Slice.Verify.Contract, expectedVerify)
			hasWaiver := false
			for _, waiver := range slice.Slice.Verify.Waivers {
				if waiver.Contract == usage.Contract {
					hasWaiver = true
					v.validateWaiver(slice.Path, waiver)
				}
			}
			if !hasVerify && !hasWaiver {
				v.result.AddError(slice.Path, "contractUsage %q/%q must have verify.contract %q or waiver", usage.Contract, usage.Role, expectedVerify)
			}
		}

		for _, waiver := range slice.Slice.Verify.Waivers {
			if !sliceHasContractUsage(slice, waiver.Contract) {
				v.result.AddWarning(slice.Path, "waiver for contract %q has no matching contractUsage", waiver.Contract)
			}
		}
	}
}

func (v *validator) validateJourneys() {
	statusEntries := map[string]struct{}{}
	if v.repo.Status != nil {
		for _, entry := range v.repo.Status.Entries {
			statusEntries[entry.JourneyID] = struct{}{}
		}
	}

	for _, journey := range v.repo.Journeys {
		if journey.Journey.ID == "" {
			v.result.AddError(journey.Path, "journey.id is required")
			continue
		}
		if v.repo.Status == nil {
			v.result.AddWarning(journey.Path, "status-board.yaml is missing entry for journey %q", journey.Journey.ID)
			continue
		}
		if _, ok := statusEntries[journey.Journey.ID]; !ok {
			v.result.AddWarning(journey.Path, "status-board.yaml has no entry for journey %q", journey.Journey.ID)
		}
	}
}

func (v *validator) validateStatusBoard() {
	if v.repo.Status == nil {
		return
	}
	journeys := map[string]struct{}{}
	for _, journey := range v.repo.Journeys {
		journeys[journey.Journey.ID] = struct{}{}
	}
	for _, entry := range v.repo.Status.Entries {
		if _, ok := journeys[entry.JourneyID]; !ok {
			v.result.AddWarning(v.repo.Status.Path, "status entry references unknown journey %q", entry.JourneyID)
		}
	}
}

func (v *validator) validateL0Dependencies() {
	for _, cell := range v.repo.Cells {
		for _, dep := range cell.Cell.L0Dependencies {
			target, ok := v.cells[dep.Cell]
			if !ok {
				v.result.AddError(cell.Path, "l0Dependency references unknown cell %q", dep.Cell)
				continue
			}
			if target.Cell.ConsistencyLevel != "L0" {
				v.result.AddError(cell.Path, "l0Dependency target %q must be an L0 cell", dep.Cell)
			}
			srcAssembly := v.assembliesByCell[cell.EffectiveID()]
			dstAssembly := v.assembliesByCell[target.EffectiveID()]
			if srcAssembly == "" || dstAssembly == "" || srcAssembly != dstAssembly {
				v.result.AddError(cell.Path, "l0Dependency %q must be in the same assembly", dep.Cell)
			}
		}
	}
}

func (v *validator) validateWaiver(path string, waiver Waiver) {
	if waiver.Contract == "" {
		v.result.AddError(path, "waiver.contract is required")
	}
	if waiver.Owner == "" {
		v.result.AddError(path, "waiver.owner is required for contract %q", waiver.Contract)
	}
	if waiver.Reason == "" {
		v.result.AddError(path, "waiver.reason is required for contract %q", waiver.Contract)
	}
	if waiver.ExpiresAt == "" {
		v.result.AddError(path, "waiver.expiresAt is required for contract %q", waiver.Contract)
		return
	}
	expiry, err := time.Parse("2006-01-02", waiver.ExpiresAt)
	if err != nil {
		v.result.AddError(path, "waiver.expiresAt %q must be YYYY-MM-DD", waiver.ExpiresAt)
		return
	}
	today := time.Now().UTC().Truncate(24 * time.Hour)
	if expiry.Before(today) {
		v.result.AddError(path, "waiver for contract %q expired on %s", waiver.Contract, waiver.ExpiresAt)
	}
}

func (v *validator) actorExists(id string) bool {
	if _, ok := v.cells[id]; ok {
		return true
	}
	_, ok := v.repo.Actors[id]
	return ok
}

func sliceHasContractUsage(slice *SliceFile, contractID string) bool {
	for _, usage := range slice.Slice.ContractUsages {
		if usage.Contract == contractID {
			return true
		}
	}
	return false
}

func validRoleForKind(kind, role string) bool {
	switch kind {
	case "http":
		return role == "serve" || role == "call"
	case "event":
		return role == "publish" || role == "subscribe"
	case "command":
		return role == "handle" || role == "invoke"
	case "projection":
		return role == "provide" || role == "read"
	default:
		return false
	}
}

func isProviderRole(kind, role string) bool {
	switch kind {
	case "http":
		return role == "serve"
	case "event":
		return role == "publish"
	case "command":
		return role == "handle"
	case "projection":
		return role == "provide"
	default:
		return false
	}
}

func actorListContains(actors []string, want string) bool {
	for _, actor := range actors {
		if actor == "*" || actor == want {
			return true
		}
	}
	return false
}

func splitContractID(id string) []string {
	if id == "" {
		return nil
	}
	return strings.Split(id, ".")
}

func fileExists(path string) bool {
	stat, err := os.Stat(path)
	return err == nil && !stat.IsDir()
}

func repositoryRoot(metaRoot string) string {
	absRoot, err := filepath.Abs(metaRoot)
	if err != nil {
		return metaRoot
	}
	if filepath.Base(absRoot) == "src" {
		return filepath.Dir(absRoot)
	}
	return absRoot
}
