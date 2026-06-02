// Package cellgen renders cell_gen.go and slice_gen.go from cell.yaml /
// slice.yaml metadata. It plugs into the tools/codegen framework: yaml →
// CellGenSpec / SliceGenSpec → text/template → goimports → disk.
//
// It is the first codegen adapter; future adapters (contract DTO,
// marker reverse-gen) will live alongside under tools/codegen/.
//
// # Usage
//
// Entry point: cellgen.Generate. BuildCellSpec and BuildSliceSpec are exposed
// for unit testing the spec-building logic in isolation; production callers
// should use Generate.
package cellgen

// CellGenSpec is the rendering input for cell.tmpl. It is the projection of
// CellMeta + child slices that the template needs to emit cell_gen.go.
//
// Fields are deterministically ordered (declaration order matters for diff
// stability — sort upstream before populating slices).
type CellGenSpec struct {
	// Package is the Go package name for cell_gen.go.
	Package string
	// StructName is the receiver type (CellMeta.GoStructName).
	StructName string
	// CellID is CellMeta.ID — used for fmt.Errorf wrapping in Init.
	CellID string
	// ConsumerGroupDefault is the cell ID, used when a SubscriptionGenSpec
	// omits its ConsumerGroup.
	ConsumerGroupDefault string
	// SourceFile is the project-relative path of the cell.yaml that drove
	// generation (e.g. "examples/todoorder/cells/ordercell/cell.yaml").
	// Rendered into the file header as "// Source: <SourceFile>" so that
	// readers of the generated file can locate the authoritative YAML.
	SourceFile string
	// RenderedMetaLiteral is the pre-rendered Go source literal for the cell's
	// metadata, of the form "&metadata.CellMeta{...}". BuildCellSpec calls
	// renderCellMetaLiteral(cell) at spec build time and assigns the result;
	// cell.tmpl emits the string verbatim into
	// `var cellMeta = {{ .RenderedMetaLiteral }}`.
	//
	// CELLGEN-LITERAL-FUNNEL-02 Hard source (highest type-system grade):
	// cell.tmpl cannot reach *metadata.CellMeta because CellGenSpec does not
	// expose it. Hand-enumeration of CellMeta fields in the template is
	// structurally unreachable — there is no data source to access. Any
	// attempt to bypass the reflect-driven renderer must change the type of
	// this field, which is a public API change and therefore review-visible.
	RenderedMetaLiteral string
	// RouteGroups holds the listener-aggregated route mounts. Each entry
	// emits one reg.RouteGroup() call.
	RouteGroups []RouteGroupGenSpec
	// Subscriptions holds the per-slice event subscriptions. Each entry
	// emits one reg.Subscribe() call (and one specEvent... var declaration).
	Subscriptions []SubscriptionGenSpec
	// WebhookReceivers holds the per-slice inbound webhook registrations.
	// Each entry emits one reg.RegisterWebhookReceiver() call.
	WebhookReceivers []WebhookReceiverGenSpec
	// WebhookDispatches holds the per-slice outbound webhook registrations.
	// Each entry emits one reg.RegisterWebhookDispatch() call.
	WebhookDispatches []WebhookDispatchGenSpec
	// Projections holds the per-slice CQRS projection registrations derived from
	// subscribe CUs that carry a non-empty projection: field. Each entry emits
	// one reg.RegisterProjection() call.
	Projections []ProjectionGenSpec
}

// RouteGroupGenSpec describes one reg.RouteGroup() call.
type RouteGroupGenSpec struct {
	// ListenerConst is the Go constant reference for the listener
	// (e.g. "cell.PrimaryListener"). Rendered verbatim.
	ListenerConst string
	// Prefix is the URL prefix mounted on the listener (e.g. "/api/v1").
	Prefix string
	// SubRoutes groups handler mounts by subPath. A nil/empty SubPath
	// means the handler attaches directly to the prefix.
	SubRoutes []RouteSubGroup
}

// RouteSubGroup is one mux.Route(subPath, ...) block aggregating multiple
// slice handlers under a shared sub-path.
type RouteSubGroup struct {
	// SubPath is the path relative to the listener prefix. Empty SubPath
	// indicates direct attachment (no mux.Route wrapper).
	SubPath string
	// Mounts lists the slice handler invocations inside the closure,
	// preserving declaration order across slices for diff stability.
	Mounts []RouteSliceMount
}

// RouteSliceMount is a single slice-handler invocation inside a sub-route
// closure: c.<HandlerField>.<Method>(s).
type RouteSliceMount struct {
	HandlerField string
	// Method is the registration method on the handler. Zero value is unsafe —
	// populate via BuildCellSpec which substitutes "RegisterRoutes" when the
	// YAML omits it.
	Method string
}

// SubscriptionGenSpec describes one reg.Subscribe() call.
type SubscriptionGenSpec struct {
	// ContractID is the full event contract id, e.g. "event.config.entry-upserted.v1".
	ContractID string
	// Transport is the event transport (default "amqp" — the current GoCell
	// convention; future contracts may override per kind).
	Transport string
	// SliceID identifies the slice owning the handler — used for
	// cell.WithSubscriptionSliceID().
	SliceID string
	// HandlerExpr is the dotted handler reference e.g. "c.subscribeSvc.HandleEntryUpserted".
	HandlerExpr string
	// ConsumerGroup is the broker consumer-group identifier. When empty
	// the renderer falls back to CellGenSpec.ConsumerGroupDefault.
	ConsumerGroup string
	// SubscriptionPackage is the Go import path for the generated contract package
	// that provides NewSubscription, e.g.
	// "github.com/ghbvf/gocell/generated/contracts/event/order-created/v1".
	// Populated by BuildCellSpec from the contract id via contractIDToImportPath.
	SubscriptionPackage string
	// SubscriptionAlias is the import alias used in cell_gen.go for SubscriptionPackage
	// (e.g. "sub0", "sub1") to avoid package name collisions between multiple v1 packages.
	SubscriptionAlias string
}

// WebhookReceiverGenSpec describes one reg.RegisterWebhookReceiver() call.
type WebhookReceiverGenSpec struct {
	// ContractID is the webhook contract id, e.g. "webhook.stripe.payment-events.v1".
	ContractID string
	// SliceID identifies the slice owning the handler — the primary sort key so
	// output ordering is deterministic even when one contract is wired by more
	// than one slice (mirrors SubscriptionGenSpec).
	SliceID string
	// SourceID is the secret-isolation key (slice.yaml contractUsages.sourceID).
	SourceID string
	// HandlerExpr is the dotted handler reference, e.g. "c.stripeSvc.HandleStripeEvent".
	HandlerExpr string

	// Runtime configuration baked from contract.yaml at code-generation time.
	// PathPattern is the HTTP path the receiver runtime mounts
	// (contract.yaml endpoints.inbound.pathPattern).
	PathPattern string
	// DeliveryIDHeader is the HTTP header name carrying the per-delivery
	// identifier (contract.yaml signature.deliveryIDHeader).
	DeliveryIDHeader string
	// TimestampHeader is the HTTP header name carrying the unix-seconds
	// timestamp (contract.yaml signature.timestampHeader).
	TimestampHeader string
	// SignatureHeader is the HTTP header name carrying the HMAC signature tokens
	// (contract.yaml signature.signatureHeader).
	SignatureHeader string
	// ToleranceSeconds is the bidirectional timestamp window in seconds
	// (contract.yaml signature.toleranceSeconds). Must be > 0.
	ToleranceSeconds int64
	// MaxBodyBytes is the maximum accepted request body size in bytes
	// (contract.yaml payload.maxBodyBytes). Must be > 0.
	MaxBodyBytes int64
}

// WebhookDispatchGenSpec describes one reg.RegisterWebhookDispatch() call.
type WebhookDispatchGenSpec struct {
	// ContractID is the webhook contract id, e.g. "webhook.shopify.orders.v1".
	ContractID string
	// SliceID identifies the slice owning the selector — the primary sort key so
	// output ordering is deterministic even when one contract is wired by more
	// than one slice (mirrors SubscriptionGenSpec).
	SliceID string
	// SourceID is the signing-secret source (slice.yaml contractUsages.sourceID).
	SourceID string
	// SelectorExpr is the dotted selector reference, e.g. "c.shopifySvc.ShopifyTarget".
	SelectorExpr string
}

// ProjectionGenSpec describes one reg.RegisterProjection() call derived from a
// subscribe CU carrying a projection: field.
type ProjectionGenSpec struct {
	ContractID   string // event contract id (input stream); Spec.Kind=="event"
	SliceID      string // primary sort key (mirrors SubscriptionGenSpec)
	ProjectionID string // snake_case probe-name; half of checkpoint key
	ApplyExpr    string // dotted apply ref, e.g. "c.projSvc.HandleOrderCreated"
	OnResetExpr  string // dotted onReset ref or "" (renders nil)
	// Populated by EnrichProjectionsWithModulePath (post-build, FS-derived):
	SpecPackage string // import path of the generated event contract package
	SpecAlias   string // "proj0","proj1",... unique import alias
}

// SliceGenSpec is the rendering input for slice.tmpl. It declares the
// canonical Service interface a slice must implement so that the cell can
// call its handler methods through a typed reference, and projects the
// parsed slice.yaml into a typed `sliceMeta` literal that the cell
// composition root consumes via cell.MustNewBaseSliceFromMeta.
type SliceGenSpec struct {
	// Package is the Go package name for slice_gen.go.
	Package string
	// CellID identifies the parent cell (used in interface comment).
	CellID string
	// SliceID identifies this slice (used in interface comment).
	SliceID string
	// SourceFile is the project-relative path of the slice.yaml that drove
	// generation (e.g. "examples/todoorder/cells/ordercell/slices/ordercreate/slice.yaml").
	// Rendered into the file header as "// Source: <SourceFile>".
	SourceFile string
	// Handlers lists the handler methods the slice's service must provide.
	// Order is deterministic to keep generated diff stable.
	Handlers []SliceHandlerSpec
	// RenderedMetaLiteral is the pre-rendered Go source literal for the
	// slice's metadata.SliceMeta value (the typed projection of slice.yaml).
	// BuildSliceSpec invokes renderSliceMetaLiteral(s) at spec build time and
	// assigns the result; slice.tmpl emits `var sliceMeta = {{ .RenderedMetaLiteral }}`.
	RenderedMetaLiteral string
}

// SliceHandlerSpec describes one method on the slice Service interface
// generated in slice_gen.go.
type SliceHandlerSpec struct {
	// MethodName is the Go method name, e.g. "HandleOrderCreated".
	MethodName string
	// ContractID is the contract that triggers the handler — included as a
	// godoc reference on the generated interface method.
	ContractID string
}

// HealthzGenSpec is the rendering input for healthz_gen.tmpl. It projects the
// cell id and package needed to emit healthz_gen.go — the typed
// RegisterReadiness helper that is the sole sanctioned way for a cell to
// register its repo readiness probe (locked by PROBENAME-SEALED-FUNNEL-01).
//
// The file is emitted unconditionally for every cell that opts into codegen
// (GoStructName present). Cells that do not have a primary repository simply
// never call RegisterReadiness; exported helpers do not trigger an
// unused-symbol compile error.
type HealthzGenSpec struct {
	// Package is the Go package name for healthz_gen.go (= CellMeta.Dir).
	Package string
	// CellID is the cell id — used to construct the ProbeRepoReady constant
	// value ("<cellid>_repo_ready") and the godoc comment.
	CellID string
	// SourceFile is the project-relative path of the cell.yaml that drove
	// generation. Rendered into the file header as "// Source: <SourceFile>".
	SourceFile string
}
