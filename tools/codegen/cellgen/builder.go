package cellgen

import (
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/ghbvf/gocell/framework/kernel/metadata"
	"github.com/ghbvf/gocell/framework/kernel/webhook"
	"github.com/ghbvf/gocell/framework/pkg/contractpath"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/tools/codegen/contractgen"
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

// projectionIDPattern mirrors kernel/healthz probe-name (snake_case). cellgen
// validates here so a bad projection: id fails at codegen, not bootstrap.
// Duplicated (not imported) to keep cellgen free of the healthz dep, matching
// the existing local-regexp convention in this file.
var projectionIDPattern = regexp.MustCompile(`^[a-z][a-z0-9]*(?:_[a-z0-9]+)*$`)

// goLocalIdentPattern matches valid Go local (unexported) identifiers.
// Used to validate HandlerField, which is derived from AST field names but
// still validated defensively to catch any unexpected input.
var goLocalIdentPattern = regexp.MustCompile(`^[a-zA-Z_][A-Za-z0-9_]*$`)

// msgUndeclaredListener is the errcode message for a route that references
// a listener not declared via +cell:listener in cell.go.
const msgUndeclaredListener = "cellgen build: route references undeclared listener" +
	" (declare with +cell:listener marker in cell.go, or remove the +slice:route marker)"

// Contract-usage role names cellgen recognizes when scanning slice.yaml.
// They mirror the kernel/cellvocab role vocabulary but are declared locally
// because cellgen does not import cellvocab (the whole package resolves roles
// via bare slice.yaml strings). Each value is used ≥3 times across builder.go.
const (
	roleSubscribe       = "subscribe"
	roleWebhookReceive  = "webhook-receive"
	roleWebhookDispatch = "webhook-dispatch"
	roleServe           = "serve"
)

// projectionSourceSagaJournal / projectionSourceOutbox mirror
// cellvocab.ProjectionSourceSagaJournal / ProjectionSourceOutbox, declared locally
// for the same reason as the role consts above (cellgen resolves slice.yaml via
// bare strings). Used by the builder kind-check, the import enrichment skip, and
// ProjectionGenSpec.IsSagaJournal (≥3 uses).
const (
	projectionSourceSagaJournal = "saga-journal"
	projectionSourceOutbox      = "outbox"
)

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
//
//nolint:funlen // pipeline of independent build steps; each step is ≤10 lines; extraction adds more lines than it removes
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

	projections, err := buildProjectionsFromSlices(p, cellID, fieldIndex)
	if err != nil {
		return nil, err
	}
	spec.Projections = projections

	grpcServices, err := buildGrpcServicesFromSlices(p, cellID, fieldIndex)
	if err != nil {
		return nil, err
	}
	spec.GrpcServices = grpcServices

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
	//
	// Projection CUs (cu.Projection != "") are intentionally excluded: their
	// handler signature is cell.ProjectionApply (returns error), enforced
	// structurally at the reg.RegisterProjection(…NewProjectionRequest(…)) callsite
	// in cell_gen.go. Including them here would render a conflicting
	// "…HandleResult" method with the same name in eventHandlerService, making the
	// generated projection slice uncompilable (double-signature conflict).
	// This mirrors the skip predicate already used in buildSubscriptionsFromSlices.
	seen := make(map[string]bool)
	for _, cu := range s.ContractUsages {
		if cu.Role != roleSubscribe || cu.Projection != "" {
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

// buildSpecsFromSlices scans every slice belonging to cellID, converts each
// contractUsage with the matching role via build, and returns the resulting
// specs sorted deterministically by (SliceID, ContractID). It is the shared
// kernel for the subscribe / webhook-receive / webhook-dispatch builders, which
// differ only in the role string, the per-CU builder, and the element type
// (sliceID/contractID accessors expose the two sort keys generically).
//
//nolint:gocognit // shared kernel for 4 builder roles; nil-guard on skip is intrinsic to the optional-filter design
func buildSpecsFromSlices[T any](
	p *metadata.ProjectMeta,
	cellID, role string,
	fieldIndex *CellFieldIndex,
	build func(p *metadata.ProjectMeta, cellID, sliceID string, cu metadata.ContractUsage, fieldIndex *CellFieldIndex) (T, error),
	sliceID func(T) string,
	contractID func(T) string,
	skip func(metadata.ContractUsage) bool,
) ([]T, error) {
	var out []T
	for key, s := range p.Slices {
		if !strings.HasPrefix(key, cellID+"/") {
			continue
		}
		if s == nil {
			continue
		}
		for _, cu := range s.ContractUsages {
			if cu.Role != role {
				continue
			}
			if skip != nil && skip(cu) {
				continue
			}
			spec, err := build(p, cellID, s.ID, cu, fieldIndex)
			if err != nil {
				return nil, err
			}
			out = append(out, spec)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if sliceID(out[i]) != sliceID(out[j]) {
			return sliceID(out[i]) < sliceID(out[j])
		}
		return contractID(out[i]) < contractID(out[j])
	})
	return out, nil
}

// buildSubscriptionsFromSlices scans all slices belonging to cellID and
// converts each contractUsage[role=subscribe] entry into a SubscriptionGenSpec,
// sorted by SliceID then ContractID.
func buildSubscriptionsFromSlices(p *metadata.ProjectMeta, cellID string, fieldIndex *CellFieldIndex) ([]SubscriptionGenSpec, error) {
	return buildSpecsFromSlices(p, cellID, roleSubscribe, fieldIndex, buildSubscriptionSpecFromCU,
		func(s SubscriptionGenSpec) string { return s.SliceID },
		func(s SubscriptionGenSpec) string { return s.ContractID },
		func(cu metadata.ContractUsage) bool { return cu.Projection != "" })
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

	fieldName, err := fieldIndex.resolveSliceField(cu.Field, cellID, sliceID, roleSubscribe)
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
		SliceID:       sliceID,
		HandlerExpr:   "c." + fieldName + "." + cu.Handler,
		ConsumerGroup: cu.Group,
	}, nil
}

// buildWebhookReceiversFromSlices scans all slices belonging to cellID and
// converts each contractUsage[role=webhook-receive] entry into a
// WebhookReceiverGenSpec, sorted by SliceID then ContractID.
func buildWebhookReceiversFromSlices(
	p *metadata.ProjectMeta, cellID string, fieldIndex *CellFieldIndex,
) ([]WebhookReceiverGenSpec, error) {
	return buildSpecsFromSlices(p, cellID, roleWebhookReceive, fieldIndex, buildWebhookReceiverSpecFromCU,
		func(s WebhookReceiverGenSpec) string { return s.SliceID },
		func(s WebhookReceiverGenSpec) string { return s.ContractID },
		nil)
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
	fieldName, err := fieldIndex.resolveSliceField(explicitField, cellID, sliceID, role)
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
// Runtime configuration fields (PathPattern, headers, ToleranceSeconds,
// MaxBodyBytes) are baked from contract.yaml at code-generation time via
// extractReceiverRuntimeConfig so the generated spec is single-source
// and zero-IO at runtime.
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
	if _, err := webhook.NewSourceID(cu.SourceID); err != nil {
		return WebhookReceiverGenSpec{}, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"cellgen build: webhook-receive SourceID is not a valid webhook source ID (validated by kernel/webhook.NewSourceID — single source)",
			errcode.WithDetails(
				errcode.PublicString("role", roleWebhookReceive),
				errcode.PublicString("cellID", cellID),
				errcode.PublicString("sliceID", sliceID),
				errcode.PublicString("sourceID", cu.SourceID),
			),
			errcode.WithInternal(errcode.InternalAttr("cause", err)))
	}
	fieldName, err := resolveWebhookField(p, cellID, sliceID, roleWebhookReceive, cu.Contract, cu.Field, fieldIndex)
	if err != nil {
		return WebhookReceiverGenSpec{}, err
	}
	rc, err := extractReceiverRuntimeConfig(p, cellID, sliceID, cu.Contract)
	if err != nil {
		return WebhookReceiverGenSpec{}, err
	}
	return WebhookReceiverGenSpec{
		ContractID:       cu.Contract,
		SliceID:          sliceID,
		SourceID:         cu.SourceID,
		HandlerExpr:      "c." + fieldName + "." + cu.Handler,
		PathPattern:      rc.PathPattern,
		DeliveryIDHeader: rc.DeliveryIDHeader,
		TimestampHeader:  rc.TimestampHeader,
		SignatureHeader:  rc.SignatureHeader,
		ToleranceSeconds: rc.ToleranceSeconds,
		MaxBodyBytes:     rc.MaxBodyBytes,
	}, nil
}

// receiverRuntimeConfig holds the contract.yaml-derived runtime fields baked
// into a WebhookReceiverGenSpec. Extracted into a named struct so that
// extractReceiverRuntimeConfig stays below cognitive-complexity 15 and its
// return type is self-documenting.
type receiverRuntimeConfig struct {
	PathPattern      string
	DeliveryIDHeader string
	TimestampHeader  string
	SignatureHeader  string
	ToleranceSeconds int64
	MaxBodyBytes     int64
}

// extractReceiverRuntimeConfig reads contract.yaml's signature / endpoints.inbound
// / payload sections for an inbound webhook contract and returns the runtime
// configuration that cellgen bakes into webhook.ReceiverSpec literals.
//
// Fails fast (FMT-37 defense-in-depth) when the contract is missing required
// sections or sub-fields, so a misconfigured contract.yaml produces a codegen
// error rather than a zero-valued or silently broken spec.
func extractReceiverRuntimeConfig(
	p *metadata.ProjectMeta,
	cellID, sliceID, contractID string,
) (receiverRuntimeConfig, error) {
	contract := p.Contracts[contractID] // already validated non-nil by resolveWebhookField
	details := []errcode.Option{
		errcode.WithDetails(
			errcode.PublicString("contractID", contractID),
			errcode.PublicString("cellID", cellID),
			errcode.PublicString("sliceID", sliceID),
		),
	}
	if contract.Signature == nil {
		return receiverRuntimeConfig{}, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"cellgen build: inbound webhook contract is missing signature section — required for ReceiverSpec",
			details...)
	}
	if contract.Endpoints.Inbound == nil {
		return receiverRuntimeConfig{}, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"cellgen build: inbound webhook contract is missing endpoints.inbound section — required for ReceiverSpec",
			details...)
	}
	if contract.Payload == nil {
		return receiverRuntimeConfig{}, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"cellgen build: inbound webhook contract is missing payload section — required for ReceiverSpec",
			details...)
	}
	sig := contract.Signature
	inbound := contract.Endpoints.Inbound
	if sig.DeliveryIDHeader == "" || sig.TimestampHeader == "" || sig.SignatureHeader == "" {
		return receiverRuntimeConfig{}, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"cellgen build: inbound webhook contract signature is missing required header fields",
			details...)
	}
	if inbound.PathPattern == "" {
		return receiverRuntimeConfig{}, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"cellgen build: inbound webhook contract endpoints.inbound.pathPattern is empty",
			details...)
	}
	if sig.ToleranceSeconds <= 0 {
		return receiverRuntimeConfig{}, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"cellgen build: inbound webhook contract signature.toleranceSeconds must be positive",
			details...)
	}
	if contract.Payload.MaxBodyBytes <= 0 {
		return receiverRuntimeConfig{}, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"cellgen build: inbound webhook contract payload.maxBodyBytes must be positive",
			details...)
	}
	return receiverRuntimeConfig{
		PathPattern:      inbound.PathPattern,
		DeliveryIDHeader: sig.DeliveryIDHeader,
		TimestampHeader:  sig.TimestampHeader,
		SignatureHeader:  sig.SignatureHeader,
		ToleranceSeconds: int64(sig.ToleranceSeconds), // metadata is int; spec is int64
		MaxBodyBytes:     contract.Payload.MaxBodyBytes,
	}, nil
}

// buildWebhookDispatchesFromSlices scans all slices belonging to cellID and
// converts each contractUsage[role=webhook-dispatch] entry into a
// WebhookDispatchGenSpec, sorted by SliceID then ContractID.
func buildWebhookDispatchesFromSlices(
	p *metadata.ProjectMeta, cellID string, fieldIndex *CellFieldIndex,
) ([]WebhookDispatchGenSpec, error) {
	return buildSpecsFromSlices(p, cellID, roleWebhookDispatch, fieldIndex, buildWebhookDispatchSpecFromCU,
		func(s WebhookDispatchGenSpec) string { return s.SliceID },
		func(s WebhookDispatchGenSpec) string { return s.ContractID },
		nil)
}

// buildWebhookDispatchSpecFromCU validates one ContractUsage[role=webhook-dispatch]
// and converts it to a WebhookDispatchGenSpec.
//
// The cell struct field is resolved via fieldIndex.resolveSliceField.
// SelectorExpr is rendered as `c.<fieldName>.<cu.TargetSelector>`.
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
	if _, err := webhook.NewSourceID(cu.SourceID); err != nil {
		return WebhookDispatchGenSpec{}, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"cellgen build: webhook-dispatch SourceID is not a valid webhook source ID (validated by kernel/webhook.NewSourceID — single source)",
			errcode.WithDetails(
				errcode.PublicString("role", roleWebhookDispatch),
				errcode.PublicString("cellID", cellID),
				errcode.PublicString("sliceID", sliceID),
				errcode.PublicString("sourceID", cu.SourceID),
			),
			errcode.WithInternal(errcode.InternalAttr("cause", err)))
	}
	fieldName, err := resolveWebhookField(p, cellID, sliceID, roleWebhookDispatch, cu.Contract, cu.Field, fieldIndex)
	if err != nil {
		return WebhookDispatchGenSpec{}, err
	}
	return WebhookDispatchGenSpec{
		ContractID:   cu.Contract,
		SliceID:      sliceID,
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

// buildProjectionsFromSlices scans all slices belonging to cellID and converts
// each contractUsage[role=subscribe, projection≠""] entry into a
// ProjectionGenSpec, sorted by SliceID then ProjectionID.
func buildProjectionsFromSlices(p *metadata.ProjectMeta, cellID string, fieldIndex *CellFieldIndex) ([]ProjectionGenSpec, error) {
	return buildSpecsFromSlices(p, cellID, roleSubscribe, fieldIndex, buildProjectionSpecFromCU,
		func(s ProjectionGenSpec) string { return s.SliceID },
		func(s ProjectionGenSpec) string { return s.ProjectionID },
		func(cu metadata.ContractUsage) bool { return cu.Projection == "" })
}

// buildProjectionSpecFromCU validates one ContractUsage[role=subscribe, projection≠""]
// and converts it to a ProjectionGenSpec.
//
// The cell struct field is resolved via fieldIndex.resolveSliceField.
// ApplyExpr is rendered as `c.<fieldName>.<cu.Handler>`.
// OnResetExpr is rendered as `c.<fieldName>.<cu.OnReset>` or "" when OnReset is unset.
//
//nolint:funlen // validation-only function: each block is a single guard clause; extraction would scatter semantically cohesive checks
func buildProjectionSpecFromCU(
	p *metadata.ProjectMeta,
	cellID, sliceID string,
	cu metadata.ContractUsage,
	fieldIndex *CellFieldIndex,
) (ProjectionGenSpec, error) {
	if !goExportedIdentPattern.MatchString(cu.Handler) {
		return ProjectionGenSpec{}, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"cellgen build: projection apply Handler must be a non-empty exported Go identifier",
			errcode.WithDetails(
				errcode.PublicString("cellID", cellID),
				errcode.PublicString("sliceID", sliceID),
				errcode.PublicString("handler", cu.Handler),
				errcode.PublicString("pattern", goExportedIdentPattern.String()),
			))
	}
	if !projectionIDPattern.MatchString(cu.Projection) {
		return ProjectionGenSpec{}, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"cellgen build: projection id must be a snake_case identifier",
			errcode.WithDetails(
				errcode.PublicString("cellID", cellID),
				errcode.PublicString("sliceID", sliceID),
				errcode.PublicString("projectionID", cu.Projection),
				errcode.PublicString("pattern", projectionIDPattern.String()),
			))
	}
	if cu.OnReset != "" && !goExportedIdentPattern.MatchString(cu.OnReset) {
		return ProjectionGenSpec{}, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"cellgen build: projection onReset must be an exported Go identifier",
			errcode.WithDetails(
				errcode.PublicString("cellID", cellID),
				errcode.PublicString("sliceID", sliceID),
				errcode.PublicString("onReset", cu.OnReset),
				errcode.PublicString("pattern", goExportedIdentPattern.String()),
			))
	}

	fieldName, err := fieldIndex.resolveSliceField(cu.Field, cellID, sliceID, roleSubscribe)
	if err != nil {
		return ProjectionGenSpec{}, err
	}
	if !goLocalIdentPattern.MatchString(fieldName) {
		return ProjectionGenSpec{}, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"cellgen build: projection field name must be a valid Go identifier",
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
			return ProjectionGenSpec{}, errcode.New(errcode.KindNotFound, errcode.ErrContractNotFound,
				"cellgen build: projection consumes unknown contract (looks like a scaffold stub — replace contract with a real contract id)",
				errcode.WithDetails(details...))
		}
		return ProjectionGenSpec{}, errcode.New(errcode.KindNotFound, errcode.ErrContractNotFound,
			"cellgen build: projection consumes unknown contract",
			errcode.WithDetails(details...))
	}
	if err := validateProjectionContractKind(cellID, sliceID, cu, contract.Kind); err != nil {
		return ProjectionGenSpec{}, err
	}

	onResetExpr := ""
	if cu.OnReset != "" {
		onResetExpr = "c." + fieldName + "." + cu.OnReset
	}
	return ProjectionGenSpec{
		ContractID:   cu.Contract,
		SliceID:      sliceID,
		ProjectionID: cu.Projection,
		ApplyExpr:    "c." + fieldName + "." + cu.Handler,
		OnResetExpr:  onResetExpr,
		Source:       cu.ProjectionSource,
	}, nil
}

// validateProjectionContractKind enforces the consumed contract's kind against
// the projection source: saga-journal must consume a kind=saga contract; the
// outbox path ("" or "outbox") must consume a kind=event contract. An unknown
// ProjectionSource is fail-closed here — the builder is the LAST line of the
// generation funnel, so it must not assume any non-"saga-journal" value is outbox
// (that silent fall-through would emit an outbox request for an unrecognized
// source). The parser/schema reject unknown sources upstream; this guard makes the
// funnel closed even if a value reaches the builder by another path.
func validateProjectionContractKind(cellID, sliceID string, cu metadata.ContractUsage, kind string) error {
	switch cu.ProjectionSource {
	case projectionSourceSagaJournal:
		if kind != "saga" {
			return projectionKindMismatchErr(cellID, sliceID, cu.Contract,
				"cellgen build: saga-journal projection must consume a saga contract", kind)
		}
		return nil
	case projectionSourceOutbox, "":
		// Empty and "outbox" are equivalent (cell.RegisterProjection treats
		// "" == outbox); both demand a kind=event contract. The parser already
		// requires a non-empty value, so "" only reaches here on a non-parser path.
		if kind != "event" {
			return projectionKindMismatchErr(cellID, sliceID, cu.Contract,
				"cellgen build: projection consumes non-event contract", kind)
		}
		return nil
	default:
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"cellgen build: unknown projectionSource",
			errcode.WithDetails(
				errcode.PublicString("cellID", cellID),
				errcode.PublicString("sliceID", sliceID),
				errcode.PublicString("contract", cu.Contract),
				errcode.PublicString("projectionSource", cu.ProjectionSource),
			))
	}
}

// projectionKindMismatchErr builds the shared "projection source ↔ contract kind"
// validation error (DRY: the two source branches differ only in message + the
// mismatched kind).
func projectionKindMismatchErr(cellID, sliceID, contract, msg, kind string) error {
	return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed, msg,
		errcode.WithDetails(
			errcode.PublicString("cellID", cellID),
			errcode.PublicString("sliceID", sliceID),
			errcode.PublicString("contract", contract),
			errcode.PublicString("kind", kind),
		))
}

// buildGrpcServicesFromSlices scans all slices belonging to cellID and
// converts each contractUsage[role=serve] with kind=grpc into a
// GrpcServiceGenSpec, sorted by SliceID then ContractID.
//
// Skip predicate: only skip when the contract IS KNOWN and is NOT grpc (e.g. a
// role:serve CU on a kind:http contract is a valid HTTP route handled by
// markergen — skipping it here is correct). A nil lookup (unknown contract id)
// is NOT skipped; instead buildGrpcServiceSpecFromCU / validateGrpcContractEndpoint
// returns an explicit "unknown contract" error, mirroring the subscribe path.
func buildGrpcServicesFromSlices(p *metadata.ProjectMeta, cellID string, fieldIndex *CellFieldIndex) ([]GrpcServiceGenSpec, error) {
	return buildSpecsFromSlices(p, cellID, roleServe, fieldIndex, buildGrpcServiceSpecFromCU,
		func(s GrpcServiceGenSpec) string { return s.SliceID },
		func(s GrpcServiceGenSpec) string { return s.ContractID },
		func(cu metadata.ContractUsage) bool {
			c := p.Contracts[cu.Contract]
			return c != nil && c.Kind != "grpc"
		})
}

// buildGrpcServiceSpecFromCU validates one ContractUsage[role=serve] and
// converts it to a GrpcServiceGenSpec.
//
// The cell struct field is resolved via fieldIndex.resolveSliceField.
// RegisterFunc is derived from the last dot-segment of the proto service FQN
// + "Server" (e.g. "DeviceCommandService" → "RegisterDeviceCommandServiceServer").
// ListenerConst is always "cell.PrimaryListener" for grpc-serve.
func buildGrpcServiceSpecFromCU(
	p *metadata.ProjectMeta,
	cellID, sliceID string,
	cu metadata.ContractUsage,
	fieldIndex *CellFieldIndex,
) (GrpcServiceGenSpec, error) {
	g, err := validateGrpcContractEndpoint(p, cellID, sliceID, cu.Contract)
	if err != nil {
		return GrpcServiceGenSpec{}, err
	}

	fieldName, err := fieldIndex.resolveSliceField(cu.Field, cellID, sliceID, roleServe)
	if err != nil {
		return GrpcServiceGenSpec{}, err
	}
	if !goLocalIdentPattern.MatchString(fieldName) {
		return GrpcServiceGenSpec{}, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"cellgen build: grpc-serve field name must be a valid Go identifier",
			errcode.WithDetails(
				errcode.PublicString("cellID", cellID),
				errcode.PublicString("sliceID", sliceID),
				errcode.PublicString("field", fieldName),
				errcode.PublicString("pattern", goLocalIdentPattern.String()),
			))
	}

	simpleName, err := metadata.GRPCServiceGoName(g.Service)
	if err != nil {
		return GrpcServiceGenSpec{}, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"cellgen build: grpc contract endpoints.grpc.service is not a valid exported Go name",
			errcode.WithDetails(
				errcode.PublicString("cellID", cellID),
				errcode.PublicString("sliceID", sliceID),
				errcode.PublicString("contract", cu.Contract),
			),
			errcode.WithInternal(errcode.InternalAttr("cause", err)))
	}

	// ProtoRel is repo/workspace-root-relative so EnrichGrpcServicesWithProtoInfo's
	// filepath.Join(root, ProtoRel) resolves correctly for satellite-module cells
	// (e.g. examples/iotdevice), where g.Proto is module-relative ("contracts/grpc/…")
	// but the proto lives at "<moduleBase>/contracts/grpc/…" (#1151).
	contract := p.Contracts[cu.Contract] // non-nil: validateGrpcContractEndpoint succeeded above
	return GrpcServiceGenSpec{
		ContractID:    cu.Contract,
		SliceID:       sliceID,
		HandlerField:  fieldName,
		RegisterFunc:  "Register" + simpleName + "Server",
		ListenerConst: "cell.PrimaryListener",
		ProtoRel:      metadata.GRPCProtoRepoRelPath(contract.File, g.Proto),
		Service:       g.Service,
		PublicMethods: grpcPublicMethods(g),
	}, nil
}

// grpcPublicMethods composes the per-method public overlay (#1675) into FULL
// method names (/{Service}/{Method}) for the public:true entries, keyed
// identically to the runtime registrar's attribution map so a declared-public
// method matches the served RPC exactly. Referential integrity (each name ∈ the
// proto method set) is the contractgen pre-pass's job; here we only compose.
// Returns nil when no method is public (the fail-closed default), so the template
// omits the PublicMethods field.
func grpcPublicMethods(g *metadata.GRPCTransportMeta) []string {
	var out []string
	for _, m := range g.Methods {
		if m.Public {
			out = append(out, "/"+g.Service+"/"+m.Name)
		}
	}
	return out
}

// validateGrpcContractEndpoint checks that cu.Contract exists, has kind=grpc,
// and has a fully-populated endpoints.grpc block. Returns the grpc endpoint on success.
func validateGrpcContractEndpoint(
	p *metadata.ProjectMeta,
	cellID, sliceID, contractID string,
) (*metadata.GRPCTransportMeta, error) {
	contract, ok := p.Contracts[contractID]
	if !ok {
		return nil, errcode.New(errcode.KindNotFound, errcode.ErrContractNotFound,
			"cellgen build: grpc-serve references unknown contract",
			errcode.WithDetails(
				errcode.PublicString("cellID", cellID),
				errcode.PublicString("sliceID", sliceID),
				errcode.PublicString("contract", contractID),
			))
	}
	if contract.Kind != "grpc" {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"cellgen build: grpc-serve requires a contract of kind grpc",
			errcode.WithDetails(
				errcode.PublicString("cellID", cellID),
				errcode.PublicString("sliceID", sliceID),
				errcode.PublicString("contract", contractID),
				errcode.PublicString("kind", contract.Kind),
			))
	}
	if contract.Endpoints.GRPC == nil {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"cellgen build: grpc contract is missing grpc block in endpoints",
			errcode.WithDetails(
				errcode.PublicString("cellID", cellID),
				errcode.PublicString("sliceID", sliceID),
				errcode.PublicString("contract", contractID),
			))
	}
	g := contract.Endpoints.GRPC
	if g.Service == "" {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"cellgen build: grpc contract endpoints.grpc.service is empty",
			errcode.WithDetails(
				errcode.PublicString("cellID", cellID),
				errcode.PublicString("sliceID", sliceID),
				errcode.PublicString("contract", contractID),
			))
	}
	if err := metadata.ValidateGRPCProtoPath(g.Proto); err != nil {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"cellgen build: grpc contract endpoints.grpc.proto is invalid",
			errcode.WithDetails(
				errcode.PublicString("cellID", cellID),
				errcode.PublicString("sliceID", sliceID),
				errcode.PublicString("contract", contractID),
			),
			errcode.WithInternal(errcode.InternalAttr("cause", err)))
	}
	return g, nil
}

// grpcLastSegment returns the last dot-delimited segment of a proto service
// FQN, e.g. "device.command.v1.DeviceCommandService" → "DeviceCommandService".
func grpcLastSegment(fqn string) string {
	if i := strings.LastIndex(fqn, "."); i >= 0 {
		return fqn[i+1:]
	}
	return fqn
}

// EnrichGrpcServicesWithProtoInfo populates PbImportPath and PbAlias on each
// GrpcServiceGenSpec by reading the .proto file at root/spec.ProtoRel via
// contractgen.ReadProtoServiceInfo. This is a post-build step; BuildCellSpec does
// not read the filesystem so it cannot derive the import path itself.
// The .proto is the single source of truth for the method set (#1655);
// cellgen only needs the import path and alias for the cell_gen.go registration.
//
// PbAlias is set to "grpc<index>" (0-indexed) to guarantee uniqueness even
// when multiple services share the same last path segment.
func EnrichGrpcServicesWithProtoInfo(spec *CellGenSpec, root string) error {
	for i := range spec.GrpcServices {
		gs := &spec.GrpcServices[i]
		protoAbs := filepath.Join(root, filepath.FromSlash(gs.ProtoRel))
		info, err := contractgen.ReadProtoServiceInfo(protoAbs, gs.Service)
		if err != nil {
			return errcode.Wrap(errcode.KindInvalid, errcode.ErrValidationFailed,
				"cellgen enrich grpc-serve contract", err,
				errcode.WithDetails(
					errcode.PublicString("contract", gs.ContractID),
					errcode.PublicString("slice", gs.SliceID)),
				errcode.WithInternal(errcode.InternalAttr("_",
					fmt.Sprintf("contract=%s slice=%s", gs.ContractID, gs.SliceID))))
		}
		gs.PbImportPath = info.ImportPath
		gs.PbAlias = fmt.Sprintf("grpc%d", i)

		// Referential integrity on the cellgen path (#1675 review F3): every
		// public-method overlay entry must name an RPC that exists in the proto
		// service. contractgen's validateGRPCMethodOverlay guards `gocell generate
		// contract`; this mirrors it for `gocell generate cell`, which reads the same
		// proto here. Without it, generate-cell alone could render a PublicMethods
		// entry that matches no RPC (silently inert at runtime).
		if err := validateGrpcPublicMethodsAgainstProto(gs, info); err != nil {
			return err
		}
	}
	return nil
}

// validateGrpcPublicMethodsAgainstProto fails closed when a GrpcServiceGenSpec's
// PublicMethods (composed /{service}/{method}) names an RPC absent from the proto
// service's method set. The cellgen-path sibling of
// contractgen.validateGRPCMethodOverlay (#1675 review F3).
func validateGrpcPublicMethodsAgainstProto(gs *GrpcServiceGenSpec, info contractgen.ProtoServiceInfo) error {
	if len(gs.PublicMethods) == 0 {
		return nil
	}
	protoMethods := make(map[string]struct{}, len(info.Methods))
	for _, pm := range info.Methods {
		protoMethods[pm.Name] = struct{}{}
	}
	for _, full := range gs.PublicMethods {
		name := full[strings.LastIndex(full, "/")+1:]
		if _, ok := protoMethods[name]; !ok {
			return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
				"cellgen enrich grpc-serve: endpoints.grpc.methods public entry is not an RPC of the proto service",
				errcode.WithDetails(
					errcode.PublicString("contract", gs.ContractID),
					errcode.PublicString("service", gs.Service),
					errcode.PublicString("method", name)))
		}
	}
	return nil
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
		sub.SubscriptionPackage = contractpath.ContractIDToImportPath(modulePath, sub.ContractID)
		sub.SubscriptionAlias = fmt.Sprintf("sub%d", i)
	}
}

// EnrichProjectionsWithModulePath populates SpecPackage and SpecAlias on each
// projection in the spec using the module path derived from go.mod at root.
// This is a post-build step; BuildCellSpec does not read the filesystem so it
// cannot derive the import path itself.
//
// SpecAlias is set to "proj<index>" (0-indexed) to guarantee uniqueness even
// when multiple contracts share the same last path segment.
func EnrichProjectionsWithModulePath(spec *CellGenSpec, modulePath string) {
	for i := range spec.Projections {
		pr := &spec.Projections[i]
		// saga-journal projections wire through cell.NewSagaJournalProjectionRequest
		// and reference no per-event-contract generated package, so they take no
		// import path / alias. The positional index keeps the outbox aliases stable.
		if pr.Source == projectionSourceSagaJournal {
			continue
		}
		pr.SpecPackage = contractpath.ContractIDToImportPath(modulePath, pr.ContractID)
		pr.SpecAlias = fmt.Sprintf("proj%d", i)
	}
}
