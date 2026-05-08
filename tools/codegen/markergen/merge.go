package markergen

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ghbvf/gocell/kernel/metadata"
)

// knownMarkers is the closed set of GoCell marker names.
var knownMarkers = []string{"cell:listener", "slice:route", "slice:subscribe"}

// Merge scans cell.go marker comments under projectRoot, extracts
// listener / route / subscribe declarations, and projects them into a
// per-cell WireBundle map keyed by cell ID.
//
// Cells whose cell.go is absent or declares no markers yield an empty
// WireBundle — the yaml fallback path has been removed (W2 cleanup).
// All five platform cells now declare markers; NO-WIRE-FIELDS-IN-YAML-01
// archtest enforces yaml-side absence statically.
func Merge(projectRoot string, project *metadata.ProjectMeta) (map[string]WireBundle, error) {
	result := make(map[string]WireBundle, len(project.Cells))
	var allErrs errList

	for cellID, cell := range project.Cells {
		bundle, ok, errs := loadCellBundle(projectRoot, cellID, cell)
		for _, e := range errs {
			allErrs.Append(e)
		}
		if ok {
			result[cellID] = bundle
		}
	}

	crossCheckSliceRefs(result, project, &allErrs)
	return result, allErrs.AsError()
}

// loadCellBundle reads cell.go markers for a single cell and returns the
// resulting WireBundle. The bool is true when result should be recorded
// (success or expected-absent cases); false skips writing a partial bundle
// when collect / build errors occur.
func loadCellBundle(projectRoot, cellID string, cell *metadata.CellMeta) (WireBundle, bool, []error) {
	cellGoPath := filepath.Join(projectRoot, filepath.Dir(cell.File), "cell.go")

	if _, err := os.Stat(cellGoPath); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return WireBundle{}, true, nil
		}
		// K05-10: classify os.Stat errors instead of treating all failures as absent.
		return WireBundle{}, false, []error{fmt.Errorf("cell %s: stat %s: %w", cellID, cellGoPath, err)}
	}

	relPath := cellGoPath
	if rel, err := filepath.Rel(projectRoot, cellGoPath); err == nil {
		relPath = rel
	}

	markers, err := CollectFromCellFile(cellGoPath)
	if err != nil {
		return WireBundle{}, false, []error{fmt.Errorf("cell %s: collect %s: %w", cellID, relPath, err)}
	}
	if len(markers) == 0 {
		return WireBundle{}, true, nil
	}

	bundle, errs := buildBundle(markers)
	if len(errs) > 0 {
		wrapped := make([]error, len(errs))
		for i, e := range errs {
			wrapped[i] = fmt.Errorf("cell %s: %w", cellID, e)
		}
		return WireBundle{}, false, wrapped
	}
	return bundle, true, nil
}

// crossCheckSliceRefs validates that every RouteSpec.Slice and
// SubscribeSpec.Slice references a known slice in project.Slices and that the
// referenced slice declares the expected contractUsages role (route → "serve",
// subscribe → "subscribe"). Unknown slices and missing roles produce
// actionable errors listing the declared set/roles for fast triage.
//
// K05-01a: contractUsages role must match the marker kind.
func crossCheckSliceRefs(result map[string]WireBundle, project *metadata.ProjectMeta, allErrs *errList) {
	for cellID, bundle := range result {
		cell := project.Cells[cellID]
		if cell == nil {
			continue
		}
		sliceSet := buildSliceSet(project, cellID)
		for _, r := range bundle.Routes {
			checkSliceRoleRef(cellID, r.Slice, "route", "serve", sliceSet, project, allErrs)
		}
		for _, s := range bundle.Subscribes {
			checkSliceRoleRef(cellID, s.Slice, "subscribe", "subscribe", sliceSet, project, allErrs)
		}
	}
}

// buildSliceSet returns the set of slice IDs declared under the given cell.
func buildSliceSet(project *metadata.ProjectMeta, cellID string) map[string]struct{} {
	set := make(map[string]struct{})
	for sliceKey := range project.Slices {
		if !strings.HasPrefix(sliceKey, cellID+"/") {
			continue
		}
		set[strings.TrimPrefix(sliceKey, cellID+"/")] = struct{}{}
	}
	return set
}

// checkSliceRoleRef appends errors when sliceID is unknown or its slice
// metadata does not declare the expected contractUsages role.
func checkSliceRoleRef(
	cellID, sliceID, markerKind, expectedRole string,
	sliceSet map[string]struct{},
	project *metadata.ProjectMeta,
	allErrs *errList,
) {
	if _, ok := sliceSet[sliceID]; !ok {
		allErrs.Append(fmt.Errorf("cell %s: %s marker references unknown slice %q (declared slices: %v)",
			cellID, markerKind, sliceID, sortedSliceIDs(sliceSet)))
		return
	}
	sliceMeta := project.Slices[cellID+"/"+sliceID]
	if sliceMeta != nil && !hasContractUsageRole(sliceMeta, expectedRole) {
		allErrs.Append(fmt.Errorf("cell %s: %s marker slice %q missing contractUsages role %q (declared roles: %v)",
			cellID, markerKind, sliceID, expectedRole, declaredRoles(sliceMeta)))
	}
}

// sortedSliceIDs returns a deterministically sorted slice of IDs from the set.
func sortedSliceIDs(set map[string]struct{}) []string {
	ids := make([]string, 0, len(set))
	for id := range set {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// hasContractUsageRole reports whether the slice declares at least one
// contractUsage entry with the given role.
func hasContractUsageRole(s *metadata.SliceMeta, role string) bool {
	for _, cu := range s.ContractUsages {
		if cu.Role == role {
			return true
		}
	}
	return false
}

// declaredRoles returns the sorted list of distinct roles declared in the
// slice's contractUsages, for inclusion in diagnostic error messages.
func declaredRoles(s *metadata.SliceMeta) []string {
	seen := make(map[string]struct{}, len(s.ContractUsages))
	for _, cu := range s.ContractUsages {
		seen[cu.Role] = struct{}{}
	}
	roles := make([]string, 0, len(seen))
	for r := range seen {
		roles = append(roles, r)
	}
	sort.Strings(roles)
	return roles
}

// buildBundle interprets a slice of collectedMarkers and returns the resulting
// WireBundle plus any parse/validation errors.
func buildBundle(markers []collectedMarker) (WireBundle, []error) {
	var bundle WireBundle
	var errs []error
	for _, m := range markers {
		if err := dispatchMarker(m, &bundle); err != nil {
			errs = append(errs, err)
		}
	}
	return bundle, errs
}

// dispatchMarker routes one marker to the appropriate parse function and
// appends the result to bundle. Returns a non-nil error when the marker is
// unknown, placed on the wrong target level, or otherwise malformed.
//
// Implementation note: closed-set marker dispatcher; each switch arm encodes
// schema enforcement (target level + parse + append) per marker kind. The
// cognitive load comes from the schema rules, not nested control flow.
//
//nolint:gocognit // see comment above
func dispatchMarker(m collectedMarker, bundle *WireBundle) error {
	switch m.Name {
	case "cell:listener":
		// K05-04: cell:listener must be on a type declaration, not a struct field.
		if m.Target != typeLevel {
			return fmt.Errorf("cell.go:%d: cell:listener marker must be on a type declaration, found on field %s", m.Line, m.FieldName)
		}
		ls, err := parseListener(m)
		if err != nil {
			return err
		}
		bundle.Listeners = append(bundle.Listeners, ls)
	case "slice:route":
		// K05-04: slice:route must be on a named struct field, not a type declaration.
		if m.Target != fieldLevel || m.FieldName == "" {
			target := "type declaration"
			if m.Target == fieldLevel {
				target = "anonymous field"
			}
			return fmt.Errorf("cell.go:%d: slice:route marker must be on a named struct field, found on %s", m.Line, target)
		}
		rs, err := parseRoute(m)
		if err != nil {
			return err
		}
		bundle.Routes = append(bundle.Routes, rs)
	case "slice:subscribe":
		// K05-04: slice:subscribe must be on a named struct field, not a type declaration.
		if m.Target != fieldLevel || m.FieldName == "" {
			target := "type declaration"
			if m.Target == fieldLevel {
				target = "anonymous field"
			}
			return fmt.Errorf("cell.go:%d: slice:subscribe marker must be on a named struct field, found on %s", m.Line, target)
		}
		ss, err := parseSubscribe(m)
		if err != nil {
			return err
		}
		bundle.Subscribes = append(bundle.Subscribes, ss)
	default:
		return unknownMarkerError(m)
	}
	return nil
}

// unknownMarkerError returns a descriptive error for an unrecognized marker,
// with an optional Levenshtein suggestion.
func unknownMarkerError(m collectedMarker) error {
	sug := suggestMarkerName(m.Name, knownMarkers)
	if sug != "" {
		return fmt.Errorf("cell.go:%d: unknown marker %q (did you mean %q?)", m.Line, m.Name, sug)
	}
	return fmt.Errorf("cell.go:%d: unknown marker %q", m.Line, m.Name)
}

// parseListener converts a "cell:listener" collectedMarker into a ListenerSpec.
// Required field: ref. Optional: prefix.
func parseListener(m collectedMarker) (ListenerSpec, error) {
	kv, err := parseKV(m.KVLine)
	if err != nil {
		return ListenerSpec{}, fmt.Errorf("cell.go:%d: marker %q: %w", m.Line, m.Name, err)
	}
	var errs errList
	checkUnknownFields(m, kv, []string{"ref", "prefix"}, &errs)
	ref := kv["ref"]
	if strings.TrimSpace(ref) == "" {
		errs.Append(fmt.Errorf("cell.go:%d: marker %q missing required field %q", m.Line, m.Name, "ref"))
	}
	if err := errs.AsError(); err != nil {
		return ListenerSpec{}, err
	}
	return ListenerSpec{
		Ref:    ref,
		Prefix: kv["prefix"],
	}, nil
}

// parseRoute converts a "slice:route" collectedMarker into a RouteSpec.
// Required fields: slice, subPath. Optional: listener (defaults to "cell.PrimaryListener").
// subPath must be explicitly declared; absence (typo like "subPth=") is rejected.
// An explicit empty value (subPath=) is valid and means "mount on prefix root".
func parseRoute(m collectedMarker) (RouteSpec, error) {
	kv, err := parseKV(m.KVLine)
	if err != nil {
		return RouteSpec{}, fmt.Errorf("cell.go:%d: marker %q: %w", m.Line, m.Name, err)
	}
	var errs errList
	checkUnknownFields(m, kv, []string{"slice", "listener", "subPath", "method"}, &errs)
	if strings.TrimSpace(kv["slice"]) == "" {
		errs.Append(fmt.Errorf("cell.go:%d: marker %q missing required field %q", m.Line, m.Name, "slice"))
	}
	if _, ok := kv["subPath"]; !ok {
		errs.Append(fmt.Errorf("cell.go:%d: marker %q missing required field %q", m.Line, m.Name, "subPath"))
	}
	if err := errs.AsError(); err != nil {
		return RouteSpec{}, err
	}
	listener := kv["listener"]
	if listener == "" {
		listener = "cell.PrimaryListener"
	}
	return RouteSpec{
		Slice:        kv["slice"],
		Listener:     listener,
		SubPath:      kv["subPath"],
		Method:       kv["method"],
		HandlerField: m.FieldName,
	}, nil
}

// parseSubscribe converts a "slice:subscribe" collectedMarker into a SubscribeSpec.
// Required fields: slice, topic, handler, group.
func parseSubscribe(m collectedMarker) (SubscribeSpec, error) {
	kv, err := parseKV(m.KVLine)
	if err != nil {
		return SubscribeSpec{}, fmt.Errorf("cell.go:%d: marker %q: %w", m.Line, m.Name, err)
	}
	var errs errList
	checkUnknownFields(m, kv, []string{"slice", "topic", "handler", "group"}, &errs)
	for _, f := range []string{"slice", "topic", "handler", "group"} {
		if strings.TrimSpace(kv[f]) == "" {
			errs.Append(fmt.Errorf("cell.go:%d: marker %q missing required field %q", m.Line, m.Name, f))
		}
	}
	if err := errs.AsError(); err != nil {
		return SubscribeSpec{}, err
	}
	return SubscribeSpec{
		Slice:      kv["slice"],
		Topic:      kv["topic"],
		Handler:    kv["handler"],
		Group:      kv["group"],
		SliceField: m.FieldName,
	}, nil
}

// checkUnknownFields reports an error for each key in kv that is not in allowed.
// When the Levenshtein distance to the nearest allowed field is ≤ 2, appends a
// "(did you mean %q?)" suggestion to the error message.
func checkUnknownFields(m collectedMarker, kv map[string]string, allowed []string, errs *errList) {
	allowedSet := make(map[string]struct{}, len(allowed))
	for _, a := range allowed {
		allowedSet[a] = struct{}{}
	}
	for k := range kv {
		if _, ok := allowedSet[k]; !ok {
			sug := suggestMarkerName(k, allowed)
			if sug != "" {
				errs.Append(fmt.Errorf("cell.go:%d: marker %q has unknown field %q (did you mean %q?)", m.Line, m.Name, k, sug))
			} else {
				errs.Append(fmt.Errorf("cell.go:%d: marker %q has unknown field %q", m.Line, m.Name, k))
			}
		}
	}
}
