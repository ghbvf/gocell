package cellgen

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/ghbvf/gocell/kernel/metadata"
	"github.com/ghbvf/gocell/tools/codegen/internal/pathx"
	"github.com/ghbvf/gocell/tools/codegen/markergen"
)

// stubTopicPattern matches scaffold-generated stub topic strings that look
// like "event.foo.created.v1" or contain ".foo." — indicating the developer
// forgot to replace the stub with a real contract id.
var stubTopicPattern = regexp.MustCompile(`event\.foo\.|\.created\.v1`)

// listenerRefPattern matches valid Go constant references for cell listeners,
// e.g. "cell.PrimaryListener", "cell.InternalListener". Builder validates each
// declared listener.Ref against this pattern so a typo in cell.yaml fails fast
// at codegen time with a precise error rather than producing invalid Go that
// breaks at compile time. JSON schema applies the same regex at parse time;
// this is defense in depth (parser does not run JSON schema at runtime).
var listenerRefPattern = regexp.MustCompile(`^cell\.[A-Z][A-Za-z0-9_]*$`)

// goExportedIdentPattern matches valid Go exported method names.
// Used to validate marker-supplied Method (Route) and Handler (Subscribe)
// identifiers before rendering them into cell_gen.go's
// `c.<HandlerField>.<Method>(s)` and `c.<SliceField>.<Handler>` call sites.
var goExportedIdentPattern = regexp.MustCompile(`^[A-Z][A-Za-z0-9_]*$`)

// goLocalIdentPattern matches valid Go local (unexported) identifiers.
// Used to validate HandlerField and SliceField, which are derived from AST
// field names but still validated defensively to catch any unexpected input.
var goLocalIdentPattern = regexp.MustCompile(`^[a-zA-Z_][A-Za-z0-9_]*$`)

// BuildCellSpec projects (cell.yaml + markergen.WireBundle) into the
// CellGenSpec consumed by cell.tmpl. It is the single bridge between
// parsed metadata and the renderer.
//
// bundle supplies listener / route / subscribe wire declarations derived
// from cell.go marker comments (markergen.Merge output). An empty bundle
// produces a spec with no RouteGroups and no Subscriptions.
//
// Errors:
//   - cell id not found in project
//   - cell.GoStructName missing (codegen requires explicit Go type binding)
//   - bundle listener ref does not match expected pattern
//   - bundle route references a listener not declared in bundle.Listeners
//   - bundle subscribe references a contract not declared in project
func BuildCellSpec(p *metadata.ProjectMeta, cellID string, bundle markergen.WireBundle) (*CellGenSpec, error) {
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
		SourceFile:           cell.File,
		MetadataLiteral:      buildMetadataLiteral(cell),
	}

	listenerPrefix := make(map[string]string, len(bundle.Listeners))
	listenerOrder := make([]string, 0, len(bundle.Listeners))
	for _, l := range bundle.Listeners {
		if !listenerRefPattern.MatchString(l.Ref) {
			return nil, fmt.Errorf("cellgen build: cell %q listener ref %q must match %s "+
				"(e.g. cell.PrimaryListener, cell.InternalListener)",
				cellID, l.Ref, listenerRefPattern.String())
		}
		if _, exists := listenerPrefix[l.Ref]; exists {
			return nil, fmt.Errorf("cellgen build: cell %q declares listener %q twice", cellID, l.Ref)
		}
		listenerPrefix[l.Ref] = l.Prefix
		listenerOrder = append(listenerOrder, l.Ref)
	}

	if err := validateBundleRoutes(cellID, bundle.Routes, listenerPrefix); err != nil {
		return nil, err
	}

	spec.RouteGroups = buildRouteGroupsFromBundle(bundle.Routes, listenerOrder, listenerPrefix)

	subs, err := buildSubscriptionsFromBundle(p, cellID, bundle.Subscribes)
	if err != nil {
		return nil, err
	}
	spec.Subscriptions = subs

	return spec, nil
}

// BuildSliceSpec returns the rendering input for slice.tmpl. Returns nil
// (with nil error) when the bundle declares no subscribes for this slice —
// slice_gen.go is only emitted when there is a typed handler interface to
// declare.
//
// bundle is the WireBundle for the parent cell (from markergen.Merge). Only
// the Subscribes entries whose Slice field matches sliceID are used.
func BuildSliceSpec(p *metadata.ProjectMeta, cellID, sliceID string, bundle markergen.WireBundle) (*SliceGenSpec, error) {
	if p == nil {
		return nil, fmt.Errorf("cellgen build slice: project is nil")
	}
	key := cellID + "/" + sliceID
	s, ok := p.Slices[key]
	if !ok {
		return nil, fmt.Errorf("cellgen build slice: slice %q not found", key)
	}

	// Collect subscribe entries for this slice from the bundle.
	var sliceSubs []markergen.SubscribeSpec
	for _, sub := range bundle.Subscribes {
		if sub.Slice == sliceID {
			sliceSubs = append(sliceSubs, sub)
		}
	}

	if len(sliceSubs) == 0 {
		// Intentional (nil, nil): caller (Generate) treats nil spec as
		// "skip slice_gen.go for this slice". Sentinel error would force
		// every call site to errors.Is-check the success path.
		return nil, nil //nolint:nilnil // intentional API: nil spec means "no slice_gen.go for this slice"
	}
	spec := &SliceGenSpec{
		Package:    s.Dir,
		CellID:     cellID,
		SliceID:    sliceID,
		SourceFile: s.File,
	}
	// Deduplicate handlers by method name: when multiple topics share the same
	// handler (e.g. HandleEvent for 13 audit topics), the interface only needs
	// one declaration. Duplicate method names are a compile error in Go interfaces.
	seen := make(map[string]bool, len(sliceSubs))
	for _, sub := range sliceSubs {
		if seen[sub.Handler] {
			continue
		}
		seen[sub.Handler] = true
		spec.Handlers = append(spec.Handlers, SliceHandlerSpec{
			MethodName: sub.Handler,
			ContractID: sub.Topic,
		})
	}
	sort.Slice(spec.Handlers, func(i, j int) bool { return spec.Handlers[i].MethodName < spec.Handlers[j].MethodName })
	return spec, nil
}

// validateBundleRoutes ensures every route in the bundle:
//   - references a listener declared in bundle.Listeners
//   - has a Method that is either empty (defaults to RegisterRoutes) or a valid
//     exported Go identifier (^[A-Z][A-Za-z0-9_]*$)
//   - has a HandlerField that is a valid Go local identifier (^[a-zA-Z_][A-Za-z0-9_]*$)
func validateBundleRoutes(cellID string, routes []markergen.RouteSpec, listeners map[string]string) error {
	for _, r := range routes {
		if _, ok := listeners[r.Listener]; !ok {
			return fmt.Errorf("cellgen build: cell %q slice %q route references undeclared listener %q "+
				"(declare with +cell:listener marker in cell.go, "+
				"or remove the +slice:route marker if this field should not be a route handler)",
				cellID, r.Slice, r.Listener)
		}
		// Method is optional — empty means RegisterRoutes (applied by buildRouteGroupsFromBundle).
		// Non-empty must be a valid exported identifier to compile as c.<HandlerField>.<Method>(s).
		if r.Method != "" && !goExportedIdentPattern.MatchString(r.Method) {
			return fmt.Errorf("cellgen build: cell %q slice %q route Method %q must match %s "+
				"(exported Go identifier, e.g. RegisterRoutes, HandleHTTP) or be empty to use the default",
				cellID, r.Slice, r.Method, goExportedIdentPattern.String())
		}
		// HandlerField is derived from AST field name but validated defensively.
		if !goLocalIdentPattern.MatchString(r.HandlerField) {
			return fmt.Errorf("cellgen build: cell %q slice %q route HandlerField %q must match %s "+
				"(valid Go identifier, e.g. createHandler, queryH)",
				cellID, r.Slice, r.HandlerField, goLocalIdentPattern.String())
		}
	}
	return nil
}

// buildRouteGroupsFromBundle aggregates bundle routes into one
// RouteGroupGenSpec per declared listener (in declaration order). Inside
// each group, mounts are grouped by SubPath in deterministic order.
// Within each sub-path the mounts preserve the AST field declaration order
// from cell.go — the order in which +slice:route markers appear in the struct
// reflects the intended handler registration sequence, which matches how
// chi mounts handlers (first registered wins for identical patterns).
func buildRouteGroupsFromBundle(
	routes []markergen.RouteSpec,
	listenerOrder []string,
	listenerPrefix map[string]string,
) []RouteGroupGenSpec {
	type subKey struct{ listener, subPath string }
	bySub := make(map[subKey][]RouteSliceMount)

	for _, r := range routes {
		method := r.Method
		if method == "" {
			method = "RegisterRoutes"
		}
		key := subKey{listener: r.Listener, subPath: r.SubPath}
		bySub[key] = append(bySub[key], RouteSliceMount{
			HandlerField: r.HandlerField,
			Method:       method,
		})
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

// buildSubscriptionsFromBundle converts bundle.Subscribes into deterministically
// ordered SubscriptionGenSpecs. Order: by SliceID then by contract topic.
func buildSubscriptionsFromBundle(p *metadata.ProjectMeta, cellID string, subs []markergen.SubscribeSpec) ([]SubscriptionGenSpec, error) {
	var out []SubscriptionGenSpec
	for _, sub := range subs {
		spec, err := buildSubscriptionSpecFromBundle(p, cellID, sub)
		if err != nil {
			return nil, err
		}
		out = append(out, spec)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].SliceID != out[j].SliceID {
			return out[i].SliceID < out[j].SliceID
		}
		return out[i].ContractID < out[j].ContractID
	})
	return out, nil
}

// buildSubscriptionSpecFromBundle validates one bundle subscribe entry against
// its contract and converts it to a SubscriptionGenSpec.
//
// Identifier validation:
//   - Handler must be an exported Go identifier (^[A-Z][A-Za-z0-9_]*$) so the
//     rendered `c.<SliceField>.<Handler>` compiles as an exported method call.
//   - SliceField is derived from the AST field name; validated as a local Go
//     identifier (^[a-zA-Z_][A-Za-z0-9_]*$) for defense in depth.
func buildSubscriptionSpecFromBundle(p *metadata.ProjectMeta, cellID string, sub markergen.SubscribeSpec) (SubscriptionGenSpec, error) {
	if !goExportedIdentPattern.MatchString(sub.Handler) {
		return SubscriptionGenSpec{}, fmt.Errorf("cellgen build: cell %q slice %q subscribe Handler %q must match %s "+
			"(exported Go identifier, e.g. HandleEvent, HandleOrderCreated)",
			cellID, sub.Slice, sub.Handler, goExportedIdentPattern.String())
	}
	if !goLocalIdentPattern.MatchString(sub.SliceField) {
		return SubscriptionGenSpec{}, fmt.Errorf("cellgen build: cell %q slice %q subscribe SliceField %q must match %s "+
			"(valid Go identifier, e.g. orderSvc, eventHandler)",
			cellID, sub.Slice, sub.SliceField, goLocalIdentPattern.String())
	}
	contract, ok := p.Contracts[sub.Topic]
	if !ok {
		prefix := ""
		if stubTopicPattern.MatchString(sub.Topic) {
			prefix = "looks like a scaffold stub — replace topic with a real contract id; "
		}
		return SubscriptionGenSpec{}, fmt.Errorf("cellgen build: cell %q slice %q %ssubscribes to unknown contract %q",
			cellID, sub.Slice, prefix, sub.Topic)
	}
	if contract.Kind != "event" {
		return SubscriptionGenSpec{}, fmt.Errorf("cellgen build: cell %q slice %q subscribes to non-event contract %q (kind=%s)",
			cellID, sub.Slice, sub.Topic, contract.Kind)
	}
	return SubscriptionGenSpec{
		ContractID:    sub.Topic,
		Transport:     "amqp",
		SliceID:       sub.Slice,
		HandlerExpr:   "c." + sub.SliceField + "." + sub.Handler,
		ConsumerGroup: sub.Group,
	}, nil
}

// buildMetadataLiteral projects CellMeta yaml fields into the rendering
// shape consumed by the metadata block in cell.tmpl. The smoke list is
// copied (not aliased) so downstream mutation cannot leak back into the
// parsed ProjectMeta.
func buildMetadataLiteral(cell *metadata.CellMeta) CellMetadataLiteral {
	smoke := append([]string(nil), cell.Verify.Smoke...)
	return CellMetadataLiteral{
		ID:               cell.ID,
		Type:             cell.Type,
		ConsistencyLevel: cell.ConsistencyLevel,
		DurabilityMode:   cell.DurabilityMode,
		OwnerTeam:        cell.Owner.Team,
		OwnerRole:        cell.Owner.Role,
		SchemaPrimary:    cell.Schema.Primary,
		VerifySmoke:      smoke,
		GoStructName:     cell.GoStructName,
	}
}

// readModulePath reads the Go module path from the go.mod file at root.
// Returns ("", err) if go.mod is missing or malformed.
func readModulePath(root string) (string, error) {
	f, err := os.Open(filepath.Clean(filepath.Join(root, "go.mod")))
	if err != nil {
		return "", fmt.Errorf("cellgen: open go.mod: %w", err)
	}
	defer func() { _ = f.Close() }()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if rest, ok := strings.CutPrefix(line, "module "); ok {
			return strings.TrimSpace(rest), nil
		}
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("cellgen: read go.mod: %w", err)
	}
	return "", fmt.Errorf("cellgen: module directive not found in go.mod")
}

// contractIDToImportPath converts a contract id to its generated package import path.
// "event.order-created.v1" → "<module>/generated/contracts/event/order-created/v1"
// "event.config.entry-upserted.v1" → "<module>/generated/contracts/event/config/entry-upserted/v1"
// "http.internal.foo.v1" → "<module>/generated/contracts/http/internalapi/foo/v1"
//
// Delegates internal→internalapi rewriting to pathx.ContractIDToPackagePath —
// single source of truth shared with contractgen and archtest.
func contractIDToImportPath(modulePath, contractID string) string {
	return modulePath + "/" + pathx.ContractIDToPackagePath(contractID)
}

// EnrichSubscriptionsWithModulePath populates SubscriptionPackage and
// SubscriptionAlias on each subscription in the spec using the module path
// derived from go.mod at root. This is a post-build step; BuildCellSpec does
// not read the filesystem so it cannot derive the import path itself.
//
// SubscriptionAlias is set to "sub<index>" (0-indexed) to guarantee
// uniqueness even when multiple contracts share the same last path segment
// (e.g. multiple "v1" packages).
func EnrichSubscriptionsWithModulePath(spec *CellGenSpec, modulePath string) {
	for i := range spec.Subscriptions {
		sub := &spec.Subscriptions[i]
		sub.SubscriptionPackage = contractIDToImportPath(modulePath, sub.ContractID)
		sub.SubscriptionAlias = fmt.Sprintf("sub%d", i)
	}
}
