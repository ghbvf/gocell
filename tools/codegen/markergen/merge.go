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
//
//nolint:gocognit,cyclop,funlen // sequential markergen pipeline (collect + merge + ownership cross-check); complexity intrinsic
func Merge(projectRoot string, project *metadata.ProjectMeta) (map[string]WireBundle, error) {
	result := make(map[string]WireBundle, len(project.Cells))
	var allErrs errList

	for cellID, cell := range project.Cells {
		cellGoPath := filepath.Join(projectRoot, filepath.Dir(cell.File), "cell.go")

		if _, err := os.Stat(cellGoPath); err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				// cell.go absent — empty WireBundle (expected case).
				result[cellID] = WireBundle{}
				continue
			}
			// Unexpected IO error (permission denied, broken symlink, etc.) —
			// surface instead of silently falling back to empty WireBundle.
			// K05-10: classify os.Stat errors instead of treating all failures as absent.
			allErrs.Append(fmt.Errorf("cell %s: stat %s: %w", cellID, cellGoPath, err))
			continue
		}

		relPath := cellGoPath
		if rel, err := filepath.Rel(projectRoot, cellGoPath); err == nil {
			relPath = rel
		}

		markers, err := CollectFromCellFile(cellGoPath)
		if err != nil {
			allErrs.Append(fmt.Errorf("cell %s: collect %s: %w", cellID, relPath, err))
			continue
		}

		if len(markers) == 0 {
			// cell.go exists but no markers — empty WireBundle.
			result[cellID] = WireBundle{}
			continue
		}

		bundle, errs := buildBundle(markers)
		for _, e := range errs {
			allErrs.Append(fmt.Errorf("cell %s: %w", cellID, e))
		}
		if len(errs) > 0 {
			// Skip writing a partial bundle; the errors are in allErrs.
			continue
		}
		result[cellID] = bundle
	}

	// Cross-check 1: every RouteSpec.Slice and SubscribeSpec.Slice must exist in
	// project.Slices[cellID/<sliceID>]. Unknown slice names surface actionable
	// errors listing the full declared-slice set.
	//
	// Cross-check 2 (K05-01a): the referenced slice must declare the expected
	// contractUsages role — RouteSpec.Slice must have role "serve", and
	// SubscribeSpec.Slice must have role "subscribe". Errors carry the actual
	// declared roles for fast triage.
	for cellID, bundle := range result {
		cell := project.Cells[cellID]
		if cell == nil {
			continue
		}
		sliceSet := make(map[string]struct{})
		for sliceKey := range project.Slices {
			if !strings.HasPrefix(sliceKey, cellID+"/") {
				continue
			}
			sliceID := strings.TrimPrefix(sliceKey, cellID+"/")
			sliceSet[sliceID] = struct{}{}
		}
		for _, r := range bundle.Routes {
			if _, ok := sliceSet[r.Slice]; !ok {
				allErrs.Append(fmt.Errorf("cell %s: route marker references unknown slice %q (declared slices: %v)",
					cellID, r.Slice, sortedSliceIDs(sliceSet)))
				continue
			}
			// K05-01a: slice must declare role=serve in contractUsages.
			sliceMeta := project.Slices[cellID+"/"+r.Slice]
			if sliceMeta != nil && !hasContractUsageRole(sliceMeta, "serve") {
				allErrs.Append(fmt.Errorf("cell %s: route marker slice %q missing contractUsages role %q (declared roles: %v)",
					cellID, r.Slice, "serve", declaredRoles(sliceMeta)))
			}
		}
		for _, s := range bundle.Subscribes {
			if _, ok := sliceSet[s.Slice]; !ok {
				allErrs.Append(fmt.Errorf("cell %s: subscribe marker references unknown slice %q (declared slices: %v)",
					cellID, s.Slice, sortedSliceIDs(sliceSet)))
				continue
			}
			// K05-01a: slice must declare role=subscribe in contractUsages.
			sliceMeta := project.Slices[cellID+"/"+s.Slice]
			if sliceMeta != nil && !hasContractUsageRole(sliceMeta, "subscribe") {
				allErrs.Append(fmt.Errorf("cell %s: subscribe marker slice %q missing contractUsages role %q (declared roles: %v)",
					cellID, s.Slice, "subscribe", declaredRoles(sliceMeta)))
			}
		}
	}

	return result, allErrs.AsError()
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
//nolint:gocognit // closed-set switch + target-level enforcement; complexity intrinsic
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
