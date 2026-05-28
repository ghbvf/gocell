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
	"github.com/ghbvf/gocell/pkg/contractpath"
	"github.com/ghbvf/gocell/pkg/errcode"
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
// Used to validate HandlerField, which is derived from AST field names but
// still validated defensively to catch any unexpected input.
var goLocalIdentPattern = regexp.MustCompile(`^[a-zA-Z_][A-Za-z0-9_]*$`)

// webhookSourceIDPattern matches valid webhook source IDs.
// Mirrors kernel/webhook sourceIDPattern: lowercase start, lowercase alphanumeric
// with hyphens/underscores, max 64 chars. Validated at codegen time so a malformed
// sourceID fails fast rather than being silently baked into generated source.
var webhookSourceIDPattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)

// msgUndeclaredListener is the errcode message for a route that references
// a listener not declared via +cell:listener in cell.go.
const msgUndeclaredListener = "cellgen build: route references undeclared listener" +
	" (declare with +cell:listener marker in cell.go, or remove the +slice:route marker)"

// BuildCellSpec projects (cell.yaml + markergen.WireBundle + fieldIndex) into
// the CellGenSpec consumed by cell.tmpl. It is the single bridge between
// parsed metadata and the renderer.
//
// bundle supplies listener / route wire declarations derived from cell.go
// marker comments (markergen.Merge output). An empty bundle produces a spec
// with no RouteGroups.
//
// fieldIndex indexes the cell struct's pointer fields (by slice package short
// name and by field name). It is derived by
// IndexCellStructFields(cellGoPath, goStructName). Pass nil when no
// subscriptions are expected; a nil index with subscribe CUs in slices will
// produce an error.
//
// Subscriptions are derived from slice.yaml contractUsages[role=subscribe],
// not from bundle.Subscribes (subscribe single-source flip K05 W3).
//
// Errors:
//   - cell id not found in project
//   - cell.GoStructName missing (codegen requires explicit Go type binding)
//   - bundle listener ref does not match expected pattern
//   - bundle route references a listener not declared in bundle.Listeners
//   - subscribe CU handler field empty
//   - subscribe CU references a contract not declared in project
//   - fieldIndex missing entry for subscribing slice
func BuildCellSpec(
	p *metadata.ProjectMeta,
	cellID string,
	bundle markergen.WireBundle,
	fieldIndex *CellFieldIndex,
) (*CellGenSpec, error) {
	if p == nil {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"cellgen build: project is nil")
	}
	cell, ok := p.Cells[cellID]
	if !ok {
		return nil, errcode.New(errcode.KindNotFound, errcode.ErrCellNotFound,
			"cellgen build: cell not found",
			errcode.WithDetails(errcode.PublicString("cellID", cellID)))
	}
	if cell.GoStructName.IsZero() {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"cellgen build: cell is missing goStructName in cell.yaml — required for cell_gen.go",
			errcode.WithDetails(errcode.PublicString("cellID", cellID)))
	}

	spec := &CellGenSpec{
		Package:              cell.Dir,
		StructName:           cell.GoStructName.String(),
		CellID:               cell.ID,
		ConsumerGroupDefault: cell.ID,
		SourceFile:           cell.File,
		RenderedMetaLiteral:  renderCellMetaLiteral(cell),
	}

	listenerPrefix := make(map[string]string, len(bundle.Listeners))
	listenerOrder := make([]string, 0, len(bundle.Listeners))
	for _, l := range bundle.Listeners {
		if !listenerRefPattern.MatchString(l.Ref) {
			return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
				"cellgen build: listener ref must match pattern (e.g. cell.PrimaryListener, cell.InternalListener)",
				errcode.WithDetails(
					errcode.PublicString("cellID", cellID),
					errcode.PublicString("listenerRef", l.Ref),
					errcode.PublicString("pattern", listenerRefPattern.String()),
				))
		}
		if _, exists := listenerPrefix[l.Ref]; exists {
			return nil, errcode.New(errcode.KindConflict, errcode.ErrConflict,
				"cellgen build: cell declares listener twice",
				errcode.WithDetails(
					errcode.PublicString("cellID", cellID),
					errcode.PublicString("listenerRef", l.Ref),
				))
		}
		listenerPrefix[l.Ref] = l.Prefix
		listenerOrder = append(listenerOrder, l.Ref)
	}

	if err := validateBundleRoutes(cellID, bundle.Routes, listenerPrefix); err != nil {
		return nil, err
	}

	spec.RouteGroups = buildRouteGroupsFromBundle(bundle.Routes, listenerOrder, listenerPrefix)

	subs, err := buildSubscriptionsFromSlices(p, cellID, fieldIndex)
	if err != nil {
		return nil, err
	}
	spec.Subscriptions = subs

	receivers, err := buildWebhookReceiversFromSlices(p, cellID, fieldIndex)
	if err != nil {
		return nil, err
	}
	spec.WebhookReceivers = receivers

	dispatches, err := buildWebhookDispatchesFromSlices(p, cellID, fieldIndex)
	if err != nil {
		return nil, err
	}
	spec.WebhookDispatches = dispatches

	return spec, nil
}

// BuildSliceSpec returns the rendering input for slice.tmpl. Every slice in
// the project produces a slice_gen.go with a typed `sliceMeta` literal so
// that cell composition roots can build BaseSlice through the funnel
// `cell.MustNewBaseSliceFromMeta(<slicePkg>.SliceMetadata())`. Slices that
// also declare event subscriptions via contractUsages[role=subscribe] get the
// typed eventHandlerService interface rendered alongside the metadata literal.
//
// Handlers are derived from the slice's ContractUsages (not from a WireBundle)
// as part of the subscribe single-source flip (K05 W3).
func BuildSliceSpec(p *metadata.ProjectMeta, cellID, sliceID string) (*SliceGenSpec, error) {
	if p == nil {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"cellgen build slice: project is nil")
	}
	key := cellID + "/" + sliceID
	s, ok := p.Slices[key]
	if !ok {
		return nil, errcode.New(errcode.KindNotFound, errcode.ErrSliceNotFound,
			"cellgen build slice: slice not found",
			errcode.WithDetails(errcode.PublicString("sliceKey", key)))
	}

	spec := &SliceGenSpec{
		Package:             s.Dir,
		CellID:              cellID,
		SliceID:             sliceID,
		SourceFile:          s.File,
		RenderedMetaLiteral: renderSliceMetaLiteral(s),
	}

	// Collect subscribe handlers from slice contractUsages.
	// Slices without subscribe CUs still produce slice_gen.go (sliceMeta only);
	// the handler interface block is rendered conditionally when Handlers is non-empty.
	seen := make(map[string]bool)
	for _, cu := range s.ContractUsages {
		if cu.Role != "subscribe" {
			continue
		}
		if seen[cu.Handler] {
			continue
		}
		seen[cu.Handler] = true
		spec.Handlers = append(spec.Handlers, SliceHandlerSpec{
			MethodName: cu.Handler,
			ContractID: cu.Contract,
		})
	}
	sort.Slice(spec.Handlers, func(i, j int) bool { return spec.Handlers[i].MethodName < spec.Handlers[j].MethodName })
	return spec, nil
}

// buildSubscriptionsFromSlices scans all slices belonging to cellID and
// converts each contractUsage[role=subscribe] entry into a SubscriptionGenSpec.
// fieldIndex is used to resolve the cell struct field for each subscribing slice.
// Results are sorted deterministically by SliceID then ContractID.
func buildSubscriptionsFromSlices(p *metadata.ProjectMeta, cellID string, fieldIndex *CellFieldIndex) ([]SubscriptionGenSpec, error) {
	var out []SubscriptionGenSpec
	for key, s := range p.Slices {
		if !strings.HasPrefix(key, cellID+"/") {
			continue
		}
		if s == nil {
			continue
		}
		for _, cu := range s.ContractUsages {
			if cu.Role != "subscribe" {
				continue
			}
			spec, err := buildSubscriptionSpecFromCU(p, cellID, s.ID, cu, fieldIndex)
			if err != nil {
				return nil, err
			}
			out = append(out, spec)
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

// buildSubscriptionSpecFromCU validates one ContractUsage[role=subscribe]
// and converts it to a SubscriptionGenSpec.
//
// The cell struct field is resolved via fieldIndex.resolveSliceField.
// HandlerExpr is rendered as `c.<fieldName>.<cu.Handler>`.
// ConsumerGroup is cu.Group (empty means template falls back to CellGenSpec.ConsumerGroupDefault).
func buildSubscriptionSpecFromCU(
	p *metadata.ProjectMeta,
	cellID, sliceID string,
	cu metadata.ContractUsage,
	fieldIndex *CellFieldIndex,
) (SubscriptionGenSpec, error) {
	if !goExportedIdentPattern.MatchString(cu.Handler) {
		return SubscriptionGenSpec{}, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"cellgen build: subscribe Handler must be a non-empty exported Go identifier (e.g. HandleEvent, HandleOrderCreated)",
			errcode.WithDetails(
				errcode.PublicString("cellID", cellID),
				errcode.PublicString("sliceID", sliceID),
				errcode.PublicString("handler", cu.Handler),
				errcode.PublicString("pattern", goExportedIdentPattern.String()),
			))
	}

	fieldName, err := fieldIndex.resolveSliceField(cu.Field, cellID, sliceID)
	if err != nil {
		return SubscriptionGenSpec{}, err
	}
	if !goLocalIdentPattern.MatchString(fieldName) {
		return SubscriptionGenSpec{}, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"cellgen build: subscribe field name must be a valid Go identifier (e.g. consumerSvc, eventHandler)",
			errcode.WithDetails(
				errcode.PublicString("cellID", cellID),
				errcode.PublicString("sliceID", sliceID),
				errcode.PublicString("field", fieldName),
				errcode.PublicString("pattern", goLocalIdentPattern.String()),
			))
	}

	contract, ok := p.Contracts[cu.Contract]
	if !ok {
		details := []errcode.PublicDetail{
			errcode.PublicString("cellID", cellID),
			errcode.PublicString("sliceID", sliceID),
			errcode.PublicString("contract", cu.Contract),
		}
		if stubTopicPattern.MatchString(cu.Contract) {
			return SubscriptionGenSpec{}, errcode.New(errcode.KindNotFound, errcode.ErrContractNotFound,
				"cellgen build: subscribes to unknown contract (looks like a scaffold stub — replace contract with a real contract id)",
				errcode.WithDetails(details...))
		}
		return SubscriptionGenSpec{}, errcode.New(errcode.KindNotFound, errcode.ErrContractNotFound,
			"cellgen build: subscribes to unknown contract",
			errcode.WithDetails(details...))
	}
	if contract.Kind != "event" {
		return SubscriptionGenSpec{}, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"cellgen build: subscribes to non-event contract",
			errcode.WithDetails(
				errcode.PublicString("cellID", cellID),
				errcode.PublicString("sliceID", sliceID),
				errcode.PublicString("contract", cu.Contract),
				errcode.PublicString("kind", contract.Kind),
			))
	}
	return SubscriptionGenSpec{
		ContractID:    cu.Contract,
		Transport:     "amqp",
		SliceID:       sliceID,
		HandlerExpr:   "c." + fieldName + "." + cu.Handler,
		ConsumerGroup: cu.Group,
	}, nil
}

// buildWebhookReceiversFromSlices scans all slices belonging to cellID and
// converts each contractUsage[role=webhook-receive] entry into a WebhookReceiverGenSpec.
// fieldIndex is used to resolve the cell struct field for each receiving slice.
// Results are sorted deterministically by SliceID then ContractID.
func buildWebhookReceiversFromSlices(
	p *metadata.ProjectMeta, cellID string, fieldIndex *CellFieldIndex,
) ([]WebhookReceiverGenSpec, error) {
	var out []WebhookReceiverGenSpec
	for key, s := range p.Slices {
		if !strings.HasPrefix(key, cellID+"/") {
			continue
		}
		if s == nil {
			continue
		}
		for _, cu := range s.ContractUsages {
			if cu.Role != "webhook-receive" {
				continue
			}
			spec, err := buildWebhookReceiverSpecFromCU(p, cellID, s.ID, cu, fieldIndex)
			if err != nil {
				return nil, err
			}
			out = append(out, spec)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		// sort by ContractID since WebhookReceiverGenSpec has no SliceID field
		return out[i].ContractID < out[j].ContractID
	})
	return out, nil
}

// resolveWebhookField resolves and validates the cell struct field name and
// validates the referenced contract is a webhook kind. It is the shared
// validation kernel for both buildWebhookReceiverSpecFromCU and
// buildWebhookDispatchSpecFromCU.
//
// Returns (fieldName, nil) on success; the role string ("webhook-receive" /
// "webhook-dispatch") is included in error messages for diagnostics.
func resolveWebhookField(
	p *metadata.ProjectMeta,
	cellID, sliceID, role, contractID, explicitField string,
	fieldIndex *CellFieldIndex,
) (string, error) {
	fieldName, err := fieldIndex.resolveSliceField(explicitField, cellID, sliceID)
	if err != nil {
		return "", err
	}
	if !goLocalIdentPattern.MatchString(fieldName) {
		return "", errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"cellgen build: webhook field name must be a valid Go identifier",
			errcode.WithDetails(
				errcode.PublicString("role", role),
				errcode.PublicString("cellID", cellID),
				errcode.PublicString("sliceID", sliceID),
				errcode.PublicString("field", fieldName),
				errcode.PublicString("pattern", goLocalIdentPattern.String()),
			))
	}

	contract, ok := p.Contracts[contractID]
	if !ok {
		return "", errcode.New(errcode.KindNotFound, errcode.ErrContractNotFound,
			"cellgen build: webhook usage references unknown contract",
			errcode.WithDetails(
				errcode.PublicString("role", role),
				errcode.PublicString("cellID", cellID),
				errcode.PublicString("sliceID", sliceID),
				errcode.PublicString("contract", contractID),
			))
	}
	if contract.Kind != "webhook" {
		return "", errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"cellgen build: webhook usage requires a contract of kind webhook",
			errcode.WithDetails(
				errcode.PublicString("role", role),
				errcode.PublicString("cellID", cellID),
				errcode.PublicString("sliceID", sliceID),
				errcode.PublicString("contract", contractID),
				errcode.PublicString("kind", contract.Kind),
			))
	}
	return fieldName, nil
}

// buildWebhookReceiverSpecFromCU validates one ContractUsage[role=webhook-receive]
// and converts it to a WebhookReceiverGenSpec.
//
// The cell struct field is resolved via fieldIndex.resolveSliceField.
// HandlerExpr is rendered as `c.<fieldName>.<cu.Handler>`.
//
//nolint:dupl // symmetric to buildWebhookDispatchSpecFromCU; different role, fields, and return types
func buildWebhookReceiverSpecFromCU(
	p *metadata.ProjectMeta,
	cellID, sliceID string,
	cu metadata.ContractUsage,
	fieldIndex *CellFieldIndex,
) (WebhookReceiverGenSpec, error) {
	if !goExportedIdentPattern.MatchString(cu.Handler) {
		return WebhookReceiverGenSpec{}, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"cellgen build: webhook-receive Handler must be a non-empty exported Go identifier (e.g. HandleStripeEvent)",
			errcode.WithDetails(
				errcode.PublicString("cellID", cellID),
				errcode.PublicString("sliceID", sliceID),
				errcode.PublicString("handler", cu.Handler),
				errcode.PublicString("pattern", goExportedIdentPattern.String()),
			))
	}
	if !webhookSourceIDPattern.MatchString(cu.SourceID) {
		return WebhookReceiverGenSpec{}, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"cellgen build: webhook-receive SourceID must match ^[a-z][a-z0-9_-]{0,63}$",
			errcode.WithDetails(
				errcode.PublicString("role", "webhook-receive"),
				errcode.PublicString("cellID", cellID),
				errcode.PublicString("sliceID", sliceID),
				errcode.PublicString("sourceID", cu.SourceID),
				errcode.PublicString("pattern", webhookSourceIDPattern.String()),
			))
	}
	fieldName, err := resolveWebhookField(p, cellID, sliceID, "webhook-receive", cu.Contract, cu.Field, fieldIndex)
	if err != nil {
		return WebhookReceiverGenSpec{}, err
	}
	return WebhookReceiverGenSpec{
		ContractID:  cu.Contract,
		SourceID:    cu.SourceID,
		HandlerExpr: "c." + fieldName + "." + cu.Handler,
	}, nil
}

// buildWebhookDispatchesFromSlices scans all slices belonging to cellID and
// converts each contractUsage[role=webhook-dispatch] entry into a WebhookDispatchGenSpec.
// fieldIndex is used to resolve the cell struct field for each dispatching slice.
// Results are sorted deterministically by ContractID.
func buildWebhookDispatchesFromSlices(
	p *metadata.ProjectMeta, cellID string, fieldIndex *CellFieldIndex,
) ([]WebhookDispatchGenSpec, error) {
	var out []WebhookDispatchGenSpec
	for key, s := range p.Slices {
		if !strings.HasPrefix(key, cellID+"/") {
			continue
		}
		if s == nil {
			continue
		}
		for _, cu := range s.ContractUsages {
			if cu.Role != "webhook-dispatch" {
				continue
			}
			spec, err := buildWebhookDispatchSpecFromCU(p, cellID, s.ID, cu, fieldIndex)
			if err != nil {
				return nil, err
			}
			out = append(out, spec)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].ContractID < out[j].ContractID
	})
	return out, nil
}

// buildWebhookDispatchSpecFromCU validates one ContractUsage[role=webhook-dispatch]
// and converts it to a WebhookDispatchGenSpec.
//
// The cell struct field is resolved via fieldIndex.resolveSliceField.
// SelectorExpr is rendered as `c.<fieldName>.<cu.TargetSelector>`.
//
//nolint:dupl // symmetric to buildWebhookReceiverSpecFromCU; different role, fields, and return types
func buildWebhookDispatchSpecFromCU(
	p *metadata.ProjectMeta,
	cellID, sliceID string,
	cu metadata.ContractUsage,
	fieldIndex *CellFieldIndex,
) (WebhookDispatchGenSpec, error) {
	if !goExportedIdentPattern.MatchString(cu.TargetSelector) {
		return WebhookDispatchGenSpec{}, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"cellgen build: webhook-dispatch TargetSelector must be a non-empty exported Go identifier (e.g. ShopifyTarget)",
			errcode.WithDetails(
				errcode.PublicString("cellID", cellID),
				errcode.PublicString("sliceID", sliceID),
				errcode.PublicString("targetSelector", cu.TargetSelector),
				errcode.PublicString("pattern", goExportedIdentPattern.String()),
			))
	}
	if !webhookSourceIDPattern.MatchString(cu.SourceID) {
		return WebhookDispatchGenSpec{}, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"cellgen build: webhook-dispatch SourceID must match ^[a-z][a-z0-9_-]{0,63}$",
			errcode.WithDetails(
				errcode.PublicString("role", "webhook-dispatch"),
				errcode.PublicString("cellID", cellID),
				errcode.PublicString("sliceID", sliceID),
				errcode.PublicString("sourceID", cu.SourceID),
				errcode.PublicString("pattern", webhookSourceIDPattern.String()),
			))
	}
	fieldName, err := resolveWebhookField(p, cellID, sliceID, "webhook-dispatch", cu.Contract, cu.Field, fieldIndex)
	if err != nil {
		return WebhookDispatchGenSpec{}, err
	}
	return WebhookDispatchGenSpec{
		ContractID:   cu.Contract,
		SourceID:     cu.SourceID,
		SelectorExpr: "c." + fieldName + "." + cu.TargetSelector,
	}, nil
}

// validateBundleRoutes ensures every route in the bundle:
//   - references a listener declared in bundle.Listeners
//   - has a Method that is either empty (defaults to RegisterRoutes) or a valid
//     exported Go identifier (^[A-Z][A-Za-z0-9_]*$)
//   - has a HandlerField that is a valid Go local identifier (^[a-zA-Z_][A-Za-z0-9_]*$)
func validateBundleRoutes(cellID string, routes []markergen.RouteSpec, listeners map[string]string) error {
	for _, r := range routes {
		if _, ok := listeners[r.Listener]; !ok {
			return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
				msgUndeclaredListener,
				errcode.WithDetails(
					errcode.PublicString("cellID", cellID),
					errcode.PublicString("sliceID", r.Slice),
					errcode.PublicString("listener", r.Listener),
				))
		}
		// Method is optional — empty means RegisterRoutes (applied by buildRouteGroupsFromBundle).
		// Non-empty must be a valid exported identifier to compile as c.<HandlerField>.<Method>(s).
		if r.Method != "" && !goExportedIdentPattern.MatchString(r.Method) {
			return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
				"cellgen build: route Method must be an exported Go identifier (e.g. RegisterRoutes, HandleHTTP) or empty to use the default",
				errcode.WithDetails(
					errcode.PublicString("cellID", cellID),
					errcode.PublicString("sliceID", r.Slice),
					errcode.PublicString("method", r.Method),
					errcode.PublicString("pattern", goExportedIdentPattern.String()),
				))
		}
		// HandlerField is derived from AST field name but validated defensively.
		if !goLocalIdentPattern.MatchString(r.HandlerField) {
			return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
				"cellgen build: route HandlerField must be a valid Go identifier (e.g. createHandler, queryH)",
				errcode.WithDetails(
					errcode.PublicString("cellID", cellID),
					errcode.PublicString("sliceID", r.Slice),
					errcode.PublicString("handlerField", r.HandlerField),
					errcode.PublicString("pattern", goLocalIdentPattern.String()),
				))
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

// readModulePath reads the Go module path from the go.mod file at root.
// Returns ("", err) if go.mod is missing or malformed.
func readModulePath(root string) (string, error) {
	f, err := os.Open(filepath.Clean(filepath.Join(root, "go.mod")))
	if err != nil {
		return "", errcode.Wrap(errcode.KindInternal, errcode.ErrInternal, "cellgen: open go.mod", err)
	}
	defer func() { _ = f.Close() }()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if rest, ok := strings.CutPrefix(line, "module "); ok {
			return strings.TrimSpace(rest), nil
		}
	}
	if err := sc.Err(); err != nil {
		return "", errcode.Wrap(errcode.KindInternal, errcode.ErrInternal, "cellgen: read go.mod", err)
	}
	return "", errcode.New(errcode.KindInternal, errcode.ErrInternal,
		"cellgen: module directive not found in go.mod")
}

// contractIDToImportPath converts a contract id to its generated package import path.
// "event.order-created.v1" → "<module>/generated/contracts/event/order-created/v1"
// "event.config.entry-upserted.v1" → "<module>/generated/contracts/event/config/entry-upserted/v1"
// "http.internal.foo.v1" → "<module>/generated/contracts/http/internalapi/foo/v1"
//
// Delegates internal→internalapi rewriting to pkg/contractpath —
// single source of truth shared with contractgen, kernel/governance, and archtest.
func contractIDToImportPath(modulePath, contractID string) string {
	return modulePath + "/" + contractpath.ContractIDToPackagePath(contractID)
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
