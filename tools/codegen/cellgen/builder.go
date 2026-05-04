package cellgen

import (
	"fmt"
	"sort"
	"strings"

	"github.com/ghbvf/gocell/kernel/metadata"
)

// BuildCellSpec projects (cell.yaml + matching slice.yaml entries) into the
// CellGenSpec consumed by cell.tmpl. It is the single bridge between
// parsed metadata and the renderer.
//
// Errors:
//   - cell id not found in project
//   - cell.GoStructName missing (codegen requires explicit Go type binding)
//   - slice routeMount references a listener not declared in cell.Listeners
//   - slice subscribes references a contract not declared in project
func BuildCellSpec(p *metadata.ProjectMeta, cellID string) (*CellGenSpec, error) {
	if p == nil {
		return nil, fmt.Errorf("cellgen build: project is nil")
	}
	cell, ok := p.Cells[cellID]
	if !ok {
		return nil, fmt.Errorf("cellgen build: cell %q not found", cellID)
	}
	if cell.GoStructName == "" {
		return nil, fmt.Errorf("cellgen build: cell %q is missing goStructName in cell.yaml — required for cell_gen.go", cellID)
	}

	spec := &CellGenSpec{
		Package:              cell.Dir,
		StructName:           cell.GoStructName,
		CellID:               cell.ID,
		ConsumerGroupDefault: cell.ID,
	}

	listenerPrefix := make(map[string]string, len(cell.Listeners))
	listenerOrder := make([]string, 0, len(cell.Listeners))
	for _, l := range cell.Listeners {
		if _, exists := listenerPrefix[l.Ref]; exists {
			return nil, fmt.Errorf("cellgen build: cell %q declares listener %q twice", cellID, l.Ref)
		}
		listenerPrefix[l.Ref] = l.Prefix
		listenerOrder = append(listenerOrder, l.Ref)
	}

	slices := slicesForCell(p, cellID)
	for _, s := range slices {
		if err := validateRouteMounts(cellID, s, listenerPrefix); err != nil {
			return nil, err
		}
	}

	spec.RouteGroups = buildRouteGroups(slices, listenerOrder, listenerPrefix)

	subs, err := buildSubscriptions(p, cellID, slices)
	if err != nil {
		return nil, err
	}
	spec.Subscriptions = subs

	return spec, nil
}

// BuildSliceSpec returns the rendering input for slice.tmpl. Returns nil
// (with nil error) when the slice has no subscribes — slice_gen.go is only
// emitted when there is a typed handler interface to declare.
func BuildSliceSpec(p *metadata.ProjectMeta, cellID, sliceID string) (*SliceGenSpec, error) {
	if p == nil {
		return nil, fmt.Errorf("cellgen build slice: project is nil")
	}
	key := cellID + "/" + sliceID
	s, ok := p.Slices[key]
	if !ok {
		return nil, fmt.Errorf("cellgen build slice: slice %q not found", key)
	}
	if len(s.Subscribes) == 0 {
		// Intentional (nil, nil): caller (Generate) treats nil spec as
		// "skip slice_gen.go for this slice". Sentinel error would force
		// every call site to errors.Is-check the success path.
		return nil, nil //nolint:nilnil // intentional API: nil spec means "no slice_gen.go for this slice"
	}
	spec := &SliceGenSpec{
		Package: s.Dir,
		CellID:  cellID,
		SliceID: sliceID,
	}
	for _, sub := range s.Subscribes {
		spec.Handlers = append(spec.Handlers, SliceHandlerSpec{
			MethodName: sub.Handler,
			ContractID: sub.Contract,
		})
	}
	sort.Slice(spec.Handlers, func(i, j int) bool { return spec.Handlers[i].MethodName < spec.Handlers[j].MethodName })
	return spec, nil
}

// slicesForCell returns the slices belonging to cellID, ordered by SliceID
// for deterministic codegen output.
func slicesForCell(p *metadata.ProjectMeta, cellID string) []*metadata.SliceMeta {
	var out []*metadata.SliceMeta
	for _, s := range p.Slices {
		if s.BelongsToCell == cellID {
			out = append(out, s)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// validateRouteMounts ensures every routeMount references a listener
// declared in cell.Listeners.
func validateRouteMounts(cellID string, s *metadata.SliceMeta, listeners map[string]string) error {
	for _, m := range s.RouteMounts {
		if _, ok := listeners[m.Listener]; !ok {
			return fmt.Errorf("cellgen build: cell %q slice %q references undeclared listener %q (declare in cell.yaml listeners)",
				cellID, s.ID, m.Listener)
		}
	}
	return nil
}

// buildRouteGroups aggregates routeMounts across slices into one
// RouteGroupGenSpec per declared listener (in declaration order). Inside
// each group, mounts are grouped by SubPath in deterministic order.
func buildRouteGroups(slices []*metadata.SliceMeta, listenerOrder []string, listenerPrefix map[string]string) []RouteGroupGenSpec {
	type subKey struct{ listener, subPath string }
	bySub := make(map[subKey][]RouteSliceMount)

	// Stable iteration: slices are already sorted by ID; routeMounts within
	// a slice keep their declared yaml order.
	for _, s := range slices {
		for _, m := range s.RouteMounts {
			method := m.Method
			if method == "" {
				method = "RegisterRoutes"
			}
			key := subKey{listener: m.Listener, subPath: m.SubPath}
			bySub[key] = append(bySub[key], RouteSliceMount{
				HandlerField: m.HandlerField,
				Method:       method,
			})
		}
	}

	if len(bySub) == 0 {
		return nil
	}

	// Group by listener in declaration order; inside each group sort
	// SubRoutes by SubPath for diff stability.
	out := make([]RouteGroupGenSpec, 0, len(listenerOrder))
	for _, listener := range listenerOrder {
		var subs []RouteSubGroup
		for key, mounts := range bySub {
			if key.listener != listener {
				continue
			}
			subs = append(subs, RouteSubGroup{SubPath: key.subPath, Mounts: mounts})
		}
		if len(subs) == 0 {
			continue
		}
		sort.Slice(subs, func(i, j int) bool { return subs[i].SubPath < subs[j].SubPath })
		out = append(out, RouteGroupGenSpec{
			ListenerConst: listener,
			Prefix:        listenerPrefix[listener],
			SubRoutes:     subs,
		})
	}
	return out
}

// buildSubscriptions flattens all slice subscribes into deterministically
// ordered SubscriptionGenSpecs. Order: by SliceID then by Contract.
func buildSubscriptions(p *metadata.ProjectMeta, cellID string, slices []*metadata.SliceMeta) ([]SubscriptionGenSpec, error) {
	var out []SubscriptionGenSpec
	for _, s := range slices {
		for _, sub := range s.Subscribes {
			contract, ok := p.Contracts[sub.Contract]
			if !ok {
				return nil, fmt.Errorf("cellgen build: cell %q slice %q subscribes to unknown contract %q",
					cellID, s.ID, sub.Contract)
			}
			if contract.Kind != "event" {
				return nil, fmt.Errorf("cellgen build: cell %q slice %q subscribes to non-event contract %q (kind=%s)",
					cellID, s.ID, sub.Contract, contract.Kind)
			}
			out = append(out, SubscriptionGenSpec{
				SpecVarName:   specVarName(sub.Contract),
				ContractID:    sub.Contract,
				Transport:     "amqp",
				SliceID:       s.ID,
				HandlerExpr:   "c." + sub.SliceField + "." + sub.Handler,
				ConsumerGroup: sub.ConsumerGroup,
			})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].SliceID != out[j].SliceID {
			return out[i].SliceID < out[j].SliceID
		}
		return out[i].ContractID < out[j].ContractID
	})
	return out, nil
}

// specVarName converts a contract id like "event.config.entry-upserted.v1"
// into a canonical Go identifier like "specEventConfigEntryUpserted":
//
//  1. drop a trailing version segment matching ^v\d+$
//  2. split remainder on "."
//  3. for each piece, split on "-" and CamelCase
//  4. prepend "spec" and concat
func specVarName(contractID string) string {
	parts := strings.Split(contractID, ".")
	if n := len(parts); n > 0 && isVersionSegment(parts[n-1]) {
		parts = parts[:n-1]
	}
	var sb strings.Builder
	sb.WriteString("spec")
	for _, p := range parts {
		for _, sub := range strings.Split(p, "-") {
			sb.WriteString(capitalize(sub))
		}
	}
	return sb.String()
}

func isVersionSegment(s string) bool {
	if len(s) < 2 || s[0] != 'v' {
		return false
	}
	for _, r := range s[1:] {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	runes := []rune(s)
	if runes[0] >= 'a' && runes[0] <= 'z' {
		runes[0] -= 'a' - 'A'
	}
	return string(runes)
}
