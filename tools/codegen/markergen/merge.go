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
// Transitional fallback: when a cell.go exists but declares no markers
// (or when cell.go is absent), Merge derives a WireBundle from
// ProjectMeta.Cells[*].Listeners + SliceMeta.{RouteMounts,Subscribes}.
// This preserves cellgen output during the W2→W5 wave migration.
// NO-WIRE-FIELDS-IN-YAML-01 archtest enforces yaml-side absence at ship.
//
// Drift detection (marker ↔ yaml double-declaration) is intentionally NOT
// done here — NO-WIRE-FIELDS-IN-YAML-01 archtest handles that statically.
func Merge(projectRoot string, project *metadata.ProjectMeta) (map[string]WireBundle, error) {
	result := make(map[string]WireBundle, len(project.Cells))
	var allErrs errList

	for cellID, cell := range project.Cells {
		cellGoPath := filepath.Join(projectRoot, filepath.Dir(cell.File), "cell.go")

		if _, err := os.Stat(cellGoPath); err != nil {
			// cell.go absent — use yaml fallback.
			result[cellID] = fallbackBundle(cellID, cell, project)
			continue
		}

		markers, err := CollectFromCellFile(cellGoPath)
		if err != nil {
			allErrs.Append(fmt.Errorf("cell %s: collect %s: %w", cellID, cellGoPath, err))
			continue
		}

		if len(markers) == 0 {
			// cell.go exists but no markers — use yaml fallback (transition period).
			// 过渡期 fallback；W4/W5 完成后 cell.yaml/slice.yaml 全无 wire 字段，
			// markergen 必走 marker 路径；ship 时 NO-WIRE-FIELDS-IN-YAML-01 archtest 兜底。
			result[cellID] = fallbackBundle(cellID, cell, project)
			continue
		}

		bundle, errs := buildBundle(markers)
		for _, e := range errs {
			allErrs.Append(e)
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
	checkUnknownFields(m, kv, []string{"slice", "listener", "subPath"}, &errs)
	for _, f := range []string{"slice", "subPath"} {
		if strings.TrimSpace(kv[f]) == "" {
			errs.Append(fmt.Errorf("cell.go:%d: marker %q missing required field %q", m.Line, m.Name, f))
		}
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

// fallbackBundle derives a WireBundle from ProjectMeta yaml fields.
// Used during the W2→W5 transition when cell.go has no markers yet.
func fallbackBundle(cellID string, cell *metadata.CellMeta, project *metadata.ProjectMeta) WireBundle {
	var bundle WireBundle
	for _, l := range cell.Listeners {
		bundle.Listeners = append(bundle.Listeners, ListenerSpec{
			Ref:    l.Ref,
			Prefix: l.Prefix,
		})
	}
	prefix := cellID + "/"
	for sliceKey, slice := range project.Slices {
		if !strings.HasPrefix(sliceKey, prefix) {
			continue
		}
		for _, rm := range slice.RouteMounts {
			bundle.Routes = append(bundle.Routes, RouteSpec{
				Slice:        slice.ID,
				Listener:     rm.Listener,
				SubPath:      rm.SubPath,
				HandlerField: rm.HandlerField,
			})
		}
		for _, sub := range slice.Subscribes {
			bundle.Subscribes = append(bundle.Subscribes, SubscribeSpec{
				Slice:      slice.ID,
				Topic:      sub.Contract,
				Handler:    sub.Handler,
				Group:      sub.ConsumerGroup,
				SliceField: sub.SliceField,
			})
		}
	}
	return bundle
}
