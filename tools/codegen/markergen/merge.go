package markergen

import (
	"fmt"
	"os"
	"path/filepath"
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
		cellGoPath := filepath.Join(projectRoot, filepath.Dir(cell.File), "cell.go")

		if _, err := os.Stat(cellGoPath); err != nil {
			// cell.go absent — empty WireBundle.
			result[cellID] = WireBundle{}
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
		result[cellID] = bundle
	}

	return result, allErrs.AsError()
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
// unknown or malformed.
func dispatchMarker(m collectedMarker, bundle *WireBundle) error {
	switch m.Name {
	case "cell:listener":
		ls, err := parseListener(m)
		if err != nil {
			return err
		}
		bundle.Listeners = append(bundle.Listeners, ls)
	case "slice:route":
		rs, err := parseRoute(m)
		if err != nil {
			return err
		}
		bundle.Routes = append(bundle.Routes, rs)
	case "slice:subscribe":
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
func checkUnknownFields(m collectedMarker, kv map[string]string, allowed []string, errs *errList) {
	allowedSet := make(map[string]struct{}, len(allowed))
	for _, a := range allowed {
		allowedSet[a] = struct{}{}
	}
	for k := range kv {
		if _, ok := allowedSet[k]; !ok {
			errs.Append(fmt.Errorf("cell.go:%d: marker %q has unknown field %q", m.Line, m.Name, k))
		}
	}
}
