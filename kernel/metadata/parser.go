// ref: gopkg.in/yaml.v3 decode.go — Decoder.Decode initializes a fresh
// unmarshaller per call. KnownFields is stored on *Decoder and therefore
// does NOT propagate through Node.Decode (see yaml.go func (n *Node) Decode,
// which allocates a new internal decoder with default settings). That is
// why unmarshalFile runs two separate Decoder passes rather than doing
// `root.Decode(out)` after the AST pass.
// ref: kubernetes-sigs/yaml UnmarshalStrict — takes a different route
// (yaml→json→json.Decoder.DisallowUnknownFields). We keep yaml.v3 native
// because we need yaml.Node line numbers, which the k8s path discards.

package metadata

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"path"
	"path/filepath"
	"sort"

	"gopkg.in/yaml.v3"

	"github.com/ghbvf/gocell/kernel/cellvocab"
	"github.com/ghbvf/gocell/pkg/errcode"
)

const (
	internalPathFmt         = "path=%s"
	internalIDPathQuotedFmt = "id=%q path=%s"
)

// Parser loads and parses all YAML metadata from a project root via a Locator.
//
// The Locator funnel is the single source of truth for filesystem topology
// — see kernel/metadata/locator.go. Parser does NOT walk the filesystem
// directly; it consumes []MetadataSource from Locator and dispatches each
// entry to the matching parseXxx method by SourceKind.
type Parser struct {
	root        string
	locatorOpts []LocatorOption
}

// NewParser creates a Parser that reads from the given filesystem root. The
// root should point to the project root directory (containing go.mod). Pass
// LocatorOption to override the auto-detected layout — e.g.,
// WithLocatorMode(LocatorConventional) to lock CI against accidental manifest
// detection, or WithLocatorMode(LocatorManifest) for an external-repo layout.
func NewParser(root string, opts ...LocatorOption) *Parser {
	return &Parser{root: root, locatorOpts: opts}
}

// Parse walks the real file system via Locator and loads all metadata YAML
// files. Returns a fully populated ProjectMeta.
func (p *Parser) Parse() (*ProjectMeta, error) {
	loc, err := NewLocator(p.root, p.locatorOpts...)
	if err != nil {
		return nil, err
	}
	return p.parseWith(loc)
}

// ParseFS parses from an fs.FS (for testing with fstest.MapFS or any caller
// that already has an fs.FS handle).
func (p *Parser) ParseFS(fsys fs.FS) (*ProjectMeta, error) {
	loc, err := NewLocatorFS(fsys, p.locatorOpts...)
	if err != nil {
		return nil, err
	}
	return p.parseWith(loc)
}

// parseWith drains the Locator into a fully populated ProjectMeta. The
// Locator funnels all filesystem walking + path-prefix classification; this
// method only dispatches by SourceKind and runs derivation passes.
func (p *Parser) parseWith(loc *Locator) (*ProjectMeta, error) {
	pm := &ProjectMeta{
		Cells:      make(map[string]*CellMeta),
		Slices:     make(map[string]*SliceMeta),
		Contracts:  make(map[string]*ContractMeta),
		Journeys:   make(map[string]*JourneyMeta),
		Assemblies: make(map[string]*AssemblyMeta),
		fileNodes:  make(map[string]*yaml.Node),
	}
	sources, err := loc.Discover()
	if err != nil {
		return nil, err
	}
	if len(sources) == 0 {
		// Warn so operators can distinguish a legitimately empty project from
		// a misconfigured root path or manifest that scanned nothing. Behavior
		// is unchanged: return an empty ProjectMeta with nil error.
		slog.Warn("metadata: parser discovered zero sources — verify root path and locator mode",
			slog.String("root", p.root),
		)
	}
	fsys := loc.FS()
	for _, src := range sources {
		if err := p.dispatchSource(fsys, src, pm); err != nil {
			return nil, err
		}
	}
	applyAssemblyDerivations(pm)
	deriveEventSubscribers(pm)
	if err := deriveWebhookEndpoints(pm); err != nil {
		return nil, err
	}
	if err := validateProjectionUniqueness(pm); err != nil {
		return nil, err
	}
	return pm, nil
}

// dispatchSource routes a discovered MetadataSource to its parse method.
func (p *Parser) dispatchSource(fsys fs.FS, src MetadataSource, pm *ProjectMeta) error {
	switch src.Kind {
	case SourceCell:
		return p.parseCell(fsys, src, pm)
	case SourceSlice:
		return p.parseSlice(fsys, src, pm)
	case SourceContract:
		return p.parseContract(fsys, src, pm)
	case SourceJourney:
		return p.parseJourney(fsys, src, pm)
	case SourceAssembly:
		return p.parseAssembly(fsys, src, pm)
	case SourceStatusBoard:
		return p.parseStatusBoard(fsys, src.Path, pm)
	case SourceActors:
		return p.parseActors(fsys, src.Path, pm)
	default:
		return errcode.New(errcode.KindInvalid, errcode.ErrMetadataInvalid,
			"unknown metadata source kind",
			errcode.WithInternal(errcode.InternalAttr("_",
				fmt.Sprintf("kind=%s path=%s", src.Kind, src.Path))))
	}
}

// --- individual parsers ---

func (p *Parser) parseCell(fsys fs.FS, src MetadataSource, pm *ProjectMeta) error {
	var m CellMeta
	node, err := unmarshalFile(fsys, src.Path, &m)
	if err != nil {
		return err
	}
	if shouldCacheFileNode(node) {
		pm.fileNodes[src.Path] = node
	}
	if m.ID == "" {
		return errcode.New(errcode.KindInvalid, errcode.ErrMetadataInvalid,
			"cell id is empty",
			errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf(internalPathFmt, src.Path))))
	}
	// Record the real filesystem directory so strict rules (REF-04) can
	// compare it against m.ID instead of self-comparing against the map key.
	// CellID from Locator is the directory name in conventional mode; in
	// manifest mode the locator may emit an empty CellID, in which case we
	// derive the directory name from the file path.
	if src.CellID != "" {
		m.Dir = src.CellID
	} else {
		m.Dir = path.Base(path.Dir(src.Path))
	}
	m.File = filepath.ToSlash(src.Path)
	if _, exists := pm.Cells[m.ID]; exists {
		return errcode.New(errcode.KindInvalid, errcode.ErrMetadataInvalid,
			"duplicate cell ID",
			errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf(internalIDPathQuotedFmt, m.ID, src.Path))))
	}
	pm.Cells[m.ID] = &m
	return nil
}

// parseSlice parses a slice.yaml and applies G-7 auto-derivation:
// if belongsToCell is omitted, it is inferred from MetadataSource.CellID
// (which Locator derives from conventional layout). When the locator cannot
// derive CellID (manifest mode with non-conventional layout), slice.yaml
// MUST declare belongsToCell explicitly.
func (p *Parser) parseSlice(fsys fs.FS, src MetadataSource, pm *ProjectMeta) error {
	var m SliceMeta
	node, err := unmarshalFile(fsys, src.Path, &m)
	if err != nil {
		return err
	}
	if shouldCacheFileNode(node) {
		pm.fileNodes[src.Path] = node
	}
	if m.ID == "" {
		return errcode.New(errcode.KindInvalid, errcode.ErrMetadataInvalid,
			"slice id is empty",
			errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf(internalPathFmt, src.Path))))
	}

	// G-7: prefer Locator-derived CellID, fall back to explicit field. When
	// neither is set, reject the slice — manifest mode without layout
	// derivation REQUIRES explicit belongsToCell.
	derivedCellID := src.CellID
	if m.BelongsToCell == "" {
		if derivedCellID == "" {
			return errcode.New(errcode.KindInvalid, errcode.ErrMetadataInvalid,
				"slice belongsToCell is required (no layout derivation available)",
				errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf("slice=%q path=%s",
					m.ID, src.Path))))
		}
		m.BelongsToCell = derivedCellID
	} else if derivedCellID != "" && m.BelongsToCell != derivedCellID {
		return errcode.New(errcode.KindInvalid, errcode.ErrMetadataInvalid,
			"slice belongsToCell does not match directory cell",
			errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf("slice=%q belongs_to=%q dir_cell=%q path=%s",
				m.ID, m.BelongsToCell, derivedCellID, src.Path))))
	}

	// Record filesystem truth separately from the yaml id. Strict rules
	// (FMT-16, FMT-17, REF-05) consume these fields so a path-vs-id split
	// (kebab dir paired with no-dash id, or vice versa) cannot escape the
	// governance gate. ToSlash normalises Windows backslashes so error
	// messages are cross-platform consistent.
	m.Dir = path.Base(path.Dir(src.Path))
	if derivedCellID != "" {
		m.CellDir = derivedCellID
	} else {
		m.CellDir = m.BelongsToCell
	}
	m.File = filepath.ToSlash(src.Path)

	key := m.BelongsToCell + "/" + m.ID
	if _, exists := pm.Slices[key]; exists {
		return errcode.New(errcode.KindInvalid, errcode.ErrMetadataInvalid,
			"duplicate slice ID",
			errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf(internalIDPathQuotedFmt, key, src.Path))))
	}
	// slice.yaml is the single source of truth for slice consistency level
	// (codegen funnel projects it into slice_gen.go.sliceMeta). The prior
	// "inherit from cell" fallback is removed — slice.yaml must declare
	// consistencyLevel explicitly, and cell.NewBaseSlice literals are no
	// longer the SoR. See T2 / BASESLICE-CTOR-FUNNEL-01.
	if m.ConsistencyLevel == "" {
		return errcode.New(errcode.KindInvalid, errcode.ErrMetadataInvalid,
			"slice consistencyLevel is empty: declare consistencyLevel: L0|L1|L2|L3|L4",
			errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf("slice=%q path=%s", m.ID, src.Path))))
	}
	pm.Slices[key] = &m
	return nil
}

// parseContract parses a contract.yaml and applies G-7 auto-derivation:
// if ownerCell is omitted, it is inferred from the provider endpoint based on
// the contract kind (http→server, event→publisher, command→handler,
// projection→provider). If the provider endpoint is also empty, ownerCell
// remains empty and governance rules will flag the issue.
//
// K#09 funnel: ContractMeta.Codegen defaults to true when the contract.yaml
// omits the `codegen:` key. The yaml.Node AST is inspected before the struct
// decode is finalized so an absent key (vs. an explicit `codegen: false`) is
// distinguishable. Explicit `codegen: false` is the only way to opt out.
func (p *Parser) parseContract(fsys fs.FS, src MetadataSource, pm *ProjectMeta) error {
	var m ContractMeta
	node, err := unmarshalFile(fsys, src.Path, &m)
	if err != nil {
		return err
	}
	if !contractYAMLHasKey(node, "codegen") {
		m.Codegen = true
	}
	if shouldCacheFileNode(node) {
		pm.fileNodes[src.Path] = node
	}
	if m.ID == "" {
		return errcode.New(errcode.KindInvalid, errcode.ErrMetadataInvalid,
			"contract id is empty",
			errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf(internalPathFmt, src.Path))))
	}
	// G-7: auto-derive ownerCell from provider endpoint if omitted (per contract.schema.json).
	if m.OwnerCell == "" {
		m.OwnerCell = m.ProviderEndpoint()
	}
	// Contract directory is derived uniformly from the source path (works
	// for both conventional layout and manifest mode with arbitrary layout).
	m.Dir = path.Dir(filepath.ToSlash(src.Path))
	m.File = filepath.ToSlash(src.Path)

	if err := resolveParamRefs(fsys, &m); err != nil {
		return err
	}

	if _, exists := pm.Contracts[m.ID]; exists {
		return errcode.New(errcode.KindInvalid, errcode.ErrMetadataInvalid,
			"duplicate contract ID",
			errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf(internalIDPathQuotedFmt, m.ID, src.Path))))
	}
	pm.Contracts[m.ID] = &m
	return nil
}

func (p *Parser) parseJourney(fsys fs.FS, src MetadataSource, pm *ProjectMeta) error {
	var m JourneyMeta
	node, err := unmarshalFile(fsys, src.Path, &m)
	if err != nil {
		return err
	}
	if shouldCacheFileNode(node) {
		pm.fileNodes[src.Path] = node
	}
	if m.ID == "" {
		return errcode.New(errcode.KindInvalid, errcode.ErrMetadataInvalid,
			"journey id is empty",
			errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf(internalPathFmt, src.Path))))
	}
	m.File = filepath.ToSlash(src.Path)
	if _, exists := pm.Journeys[m.ID]; exists {
		return errcode.New(errcode.KindInvalid, errcode.ErrMetadataInvalid,
			"duplicate journey ID",
			errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf(internalIDPathQuotedFmt, m.ID, src.Path))))
	}
	pm.Journeys[m.ID] = &m
	return nil
}

func (p *Parser) parseAssembly(fsys fs.FS, src MetadataSource, pm *ProjectMeta) error {
	var m AssemblyMeta
	node, err := unmarshalFile(fsys, src.Path, &m)
	if err != nil {
		return err
	}
	if shouldCacheFileNode(node) {
		pm.fileNodes[src.Path] = node
	}
	if m.ID == "" {
		return errcode.New(errcode.KindInvalid, errcode.ErrMetadataInvalid,
			"assembly id is empty",
			errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf(internalPathFmt, src.Path))))
	}
	// Record filesystem truth so strict rules (FMT-16) can compare the
	// directory segment against m.ID. The directory name is the parent of
	// the assembly.yaml file (works uniformly for assemblies/<id>/ and
	// examples/<id>/ paths).
	m.Dir = path.Base(path.Dir(filepath.ToSlash(src.Path)))
	m.File = filepath.ToSlash(src.Path)
	if _, exists := pm.Assemblies[m.ID]; exists {
		return errcode.New(errcode.KindInvalid, errcode.ErrMetadataInvalid,
			"duplicate assembly ID",
			errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf(internalIDPathQuotedFmt, m.ID, src.Path))))
	}
	pm.Assemblies[m.ID] = &m
	return nil
}

func (p *Parser) parseStatusBoard(fsys fs.FS, srcPath string, pm *ProjectMeta) error {
	var entries []StatusBoardEntry
	node, err := unmarshalFile(fsys, srcPath, &entries)
	if err != nil {
		return err
	}
	if shouldCacheFileNode(node) {
		pm.fileNodes[srcPath] = node
	}
	pm.StatusBoard = entries
	return nil
}

func (p *Parser) parseActors(fsys fs.FS, srcPath string, pm *ProjectMeta) error {
	var actors []ActorMeta
	node, err := unmarshalFile(fsys, srcPath, &actors)
	if err != nil {
		return err
	}
	if shouldCacheFileNode(node) {
		pm.fileNodes[srcPath] = node
	}
	pm.Actors = actors
	return nil
}

// maxMetadataFileSize caps a single YAML file at 1 MiB. Real metadata files
// are <50 KB; a 20× headroom guards against adversarial inputs (or the wrong
// fixture accidentally dropped into cells/ or contracts/) blowing up memory
// once the yaml.Node AST is retained on ProjectMeta.fileNodes for the life of a
// Validator.
const maxMetadataFileSize = 1 << 20 // 1 MiB

// unmarshalFile reads and decodes a YAML file from fsys.
//
// The decode is two-phase:
//  1. Decode into a *yaml.Node so the caller can cache it for location lookups
//     via metadata.Find / metadata.Locate.
//  2. Decode into `out` through a second Decoder with KnownFields(true) so that
//     unknown YAML keys (typos such as "ownerId" instead of "ownerCell") are
//     rejected. yaml.v3's Node.Decode does not inherit KnownFields from the
//     source Decoder (see yaml.go func (n *Node) Decode), so we re-parse the
//     bytes rather than calling root.Decode(out).
//
// Empty / whitespace-only files are treated as "no content" and return an
// empty document node to preserve the original behavior of empty actors.yaml
// or status-board.yaml without encoding success as nil data. Multi-document
// files are rejected. Files larger than maxMetadataFileSize are rejected before
// decoding (see that constant).
func unmarshalFile(fsys fs.FS, path string, out any) (*yaml.Node, error) {
	data, err := fs.ReadFile(fsys, path)
	if err != nil {
		return nil, errcode.Wrap(errcode.KindInvalid, errcode.ErrMetadataInvalid,
			"failed to read metadata file", err,
			errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf(internalPathFmt, path))))
	}
	if len(data) > maxMetadataFileSize {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrMetadataInvalid,
			"metadata file exceeds size limit",
			errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf("path=%s size=%d limit=%d", path, len(data), maxMetadataFileSize))))
	}

	// Phase 1: capture location-preserving AST.
	var root yaml.Node
	dec1 := yaml.NewDecoder(bytes.NewReader(data))
	if err := dec1.Decode(&root); err != nil {
		if errors.Is(err, io.EOF) {
			return emptyYAMLDocumentNode(), nil
		}
		return nil, errcode.Wrap(errcode.KindInvalid, errcode.ErrMetadataInvalid,
			"failed to parse metadata file", err,
			errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf(internalPathFmt, path))))
	}
	// Reject multi-document YAML files. Metadata files must contain exactly
	// one document; a second document after "---" would be silently ignored
	// by a single Decode call.
	tmpErr := dec1.Decode(new(yaml.Node))
	if !errors.Is(tmpErr, io.EOF) {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrMetadataInvalid,
			"unexpected multiple YAML documents in metadata file",
			errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf(internalPathFmt, path))))
	}

	// Phase 2: strict decode into target struct. This is where KnownFields(true)
	// catches typos and yaml.v3 produces "line N: field X not found in type ..."
	// errors that already carry line numbers.
	dec2 := yaml.NewDecoder(bytes.NewReader(data))
	dec2.KnownFields(true)
	if err := dec2.Decode(out); err != nil {
		if errors.Is(err, io.EOF) {
			// Unreachable: phase 1 already saw a document, so phase 2 cannot
			// be empty. Kept defensively to mirror the original behavior.
			return &root, nil
		}
		return nil, errcode.Wrap(errcode.KindInvalid, errcode.ErrMetadataInvalid,
			"failed to decode metadata file", err,
			errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf(internalPathFmt, path))))
	}

	return &root, nil
}

func emptyYAMLDocumentNode() *yaml.Node {
	return &yaml.Node{Kind: yaml.DocumentNode}
}

func shouldCacheFileNode(node *yaml.Node) bool {
	return node != nil && (node.Kind != yaml.DocumentNode || len(node.Content) != 0)
}

// contractYAMLHasKey reports whether the top-level mapping in the contract.yaml
// AST root contains the named key. Used by parseContract to distinguish an
// absent `codegen:` field (default true, K#09 funnel) from an explicit
// `codegen: false` opt-out. Walks the DocumentNode → MappingNode → key list.
func contractYAMLHasKey(node *yaml.Node, key string) bool {
	if node == nil {
		return false
	}
	root := node
	if root.Kind == yaml.DocumentNode {
		if len(root.Content) == 0 {
			return false
		}
		root = root.Content[0]
	}
	if root.Kind != yaml.MappingNode {
		return false
	}
	// MappingNode.Content alternates key/value pairs.
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Kind == yaml.ScalarNode && root.Content[i].Value == key {
			return true
		}
	}
	return false
}

// deriveEventSubscribers adds slices' owning cell IDs to the Subscribers list of
// any event contract they subscribe to via contractUsages[role=subscribe]. The
// result is the union of ActorSubscribers + cell IDs from slice.yaml
// contractUsages[role=subscribe] — deduped and sorted alphabetically.
// All event contracts are enumerated so actor-only contracts (no slice
// subscriptions) are also populated.
//
// Skip conditions (no error — other governance rules handle them):
//   - contract not found in pm.Contracts
//   - contract.Kind != "event"
func deriveEventSubscribers(pm *ProjectMeta) {
	// Build cell-subscriber index from slices.
	cellSubs := make(map[string][]string) // contractID → []cellID
	for _, sl := range pm.Slices {
		for _, cu := range sl.ContractUsages {
			if cu.Role != "subscribe" {
				continue
			}
			if _, ok := pm.Contracts[cu.Contract]; !ok {
				continue
			}
			cellSubs[cu.Contract] = append(cellSubs[cu.Contract], sl.BelongsToCell)
		}
	}
	// Enumerate ALL event contracts so actor-only contracts are also populated.
	for _, c := range pm.Contracts {
		if c.Kind != "event" {
			continue
		}
		combined := append([]string(nil), c.Endpoints.ActorSubscribers...)
		combined = append(combined, cellSubs[c.ID]...)
		c.Endpoints.Subscribers = dedupSorted(combined)
	}
}

// webhookCellIndex accumulates receiver/dispatcher cell IDs per contract.
type webhookCellIndex struct {
	receivers   map[string][]string // contractID → []cellID
	dispatchers map[string][]string // contractID → []cellID
}

func newWebhookCellIndex() *webhookCellIndex {
	return &webhookCellIndex{
		receivers:   make(map[string][]string),
		dispatchers: make(map[string][]string),
	}
}

// validateWebhookReceive validates a webhook-receive ContractUsage and records
// the owning cell in the index.
func validateWebhookReceive(cu ContractUsage, sl *SliceMeta, c *ContractMeta, idx *webhookCellIndex) error {
	const (
		msgMissingHandler  = "webhook-receive contractUsage missing required handler"
		msgMissingSourceID = "webhook-receive contractUsage missing required sourceID"
		msgWrongDirection  = "webhook-receive requires contract direction=inbound;" +
			" use role=webhook-dispatch for an outbound contract"
		msgNoInboundBlock = "webhook-receive requires contract.endpoints.inbound;" +
			" set direction=inbound or use role=webhook-dispatch for outbound"
		msgSourceIDMismatch = "webhook-receive contractUsage sourceID does not match contract inbound.sourceID"
	)
	if cu.Handler == "" {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed, msgMissingHandler,
			errcode.WithDetails(errcode.PublicString("contract", cu.Contract), errcode.PublicString("slice", sl.ID)))
	}
	if cu.SourceID == "" {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed, msgMissingSourceID,
			errcode.WithDetails(errcode.PublicString("contract", cu.Contract), errcode.PublicString("slice", sl.ID)))
	}
	if c.Direction != string(cellvocab.DirectionInbound) {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed, msgWrongDirection,
			errcode.WithDetails(
				errcode.PublicString("contract", cu.Contract),
				errcode.PublicString("slice", sl.ID),
				errcode.PublicString("direction", c.Direction),
			))
	}
	if c.Endpoints.Inbound == nil {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed, msgNoInboundBlock,
			errcode.WithDetails(
				errcode.PublicString("contract", cu.Contract),
				errcode.PublicString("slice", sl.ID),
				errcode.PublicString("direction", c.Direction),
			))
	}
	if c.Endpoints.Inbound.SourceID != "" && cu.SourceID != c.Endpoints.Inbound.SourceID {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed, msgSourceIDMismatch,
			errcode.WithDetails(
				errcode.PublicString("contract", cu.Contract),
				errcode.PublicString("slice", sl.ID),
				errcode.PublicString("cuSourceID", cu.SourceID),
				errcode.PublicString("contractSourceID", c.Endpoints.Inbound.SourceID),
			))
	}
	idx.receivers[cu.Contract] = append(idx.receivers[cu.Contract], sl.BelongsToCell)
	return nil
}

// validateWebhookDispatch validates a webhook-dispatch ContractUsage and records
// the owning cell in the index. The contract is required so the direction can be
// checked: dispatching an inbound contract is a fail-closed error (the symmetric
// counterpart of validateWebhookReceive rejecting an outbound contract).
func validateWebhookDispatch(cu ContractUsage, sl *SliceMeta, c *ContractMeta, idx *webhookCellIndex) error {
	const (
		msgMissingTargetSel = "webhook-dispatch contractUsage missing required targetSelector"
		msgMissingSourceID  = "webhook-dispatch contractUsage missing required sourceID"
		msgForbiddenHandler = "webhook-dispatch contractUsage must not set handler" +
			" (handler is forbidden for role=webhook-dispatch; use targetSelector instead)"
		msgWrongDirection = "webhook-dispatch requires contract direction=outbound;" +
			" use role=webhook-receive for an inbound contract"
	)
	if cu.Handler != "" {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed, msgForbiddenHandler,
			errcode.WithDetails(errcode.PublicString("contract", cu.Contract), errcode.PublicString("slice", sl.ID)))
	}
	if c.Direction != string(cellvocab.DirectionOutbound) {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed, msgWrongDirection,
			errcode.WithDetails(
				errcode.PublicString("contract", cu.Contract),
				errcode.PublicString("slice", sl.ID),
				errcode.PublicString("direction", c.Direction),
			))
	}
	if cu.TargetSelector == "" {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed, msgMissingTargetSel,
			errcode.WithDetails(errcode.PublicString("contract", cu.Contract), errcode.PublicString("slice", sl.ID)))
	}
	if cu.SourceID == "" {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed, msgMissingSourceID,
			errcode.WithDetails(errcode.PublicString("contract", cu.Contract), errcode.PublicString("slice", sl.ID)))
	}
	idx.dispatchers[cu.Contract] = append(idx.dispatchers[cu.Contract], sl.BelongsToCell)
	return nil
}

// deriveWebhookEndpoints populates EndpointsMeta.Receivers and
// EndpointsMeta.Dispatchers for every webhook contract from the slice
// contractUsages[role=webhook-receive] and [role=webhook-dispatch]
// respectively. The resulting lists are deduped and sorted alphabetically —
// mirroring deriveEventSubscribers.
//
// Validation rules (return error):
//   - webhook-receive requires non-empty handler AND sourceID
//   - webhook-dispatch requires non-empty targetSelector AND sourceID
//   - webhook-receive requires contract direction=inbound; webhook-dispatch
//     requires contract direction=outbound (fail-closed: dispatching an inbound
//     contract or receiving an outbound contract is rejected)
//   - webhook-receive CU sourceID must equal contract.endpoints.inbound.sourceID
//     (when an inbound block is present)
//   - webhook-receive CU on a contract with no inbound block → direction/role
//     mismatch
//
// Skip conditions (no error — other governance rules handle them):
//   - contract not found in pm.Contracts
//   - contract.Kind != "webhook"
func deriveWebhookEndpoints(pm *ProjectMeta) error {
	idx := newWebhookCellIndex()
	for _, sl := range pm.Slices {
		if err := indexWebhookSlice(sl, pm.Contracts, idx); err != nil {
			return err
		}
	}
	for _, c := range pm.Contracts {
		if c.Kind != "webhook" {
			continue
		}
		if err := finalizeWebhookContract(c, idx); err != nil {
			return err
		}
	}
	return nil
}

// finalizeWebhookContract validates a webhook contract's direction and populates
// its derived Receivers/Dispatchers from the slice index. Direction validation
// is fail-closed and applies to EVERY webhook contract, independent of whether a
// slice wires it: the per-CU validators (validateWebhookReceive/Dispatch) only
// fire for slice-referenced contracts, so an unreferenced contract with an
// empty/invalid direction would otherwise slip through.
func finalizeWebhookContract(c *ContractMeta, idx *webhookCellIndex) error {
	if c.Direction != string(cellvocab.DirectionInbound) && c.Direction != string(cellvocab.DirectionOutbound) {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"webhook contract direction must be \"inbound\" or \"outbound\"",
			errcode.WithDetails(
				errcode.PublicString("contract", c.ID),
				errcode.PublicString("direction", c.Direction),
			))
	}
	r := dedupSorted(idx.receivers[c.ID])
	if r == nil {
		r = []string{}
	}
	c.Endpoints.Receivers = r
	d := dedupSorted(idx.dispatchers[c.ID])
	if d == nil {
		d = []string{}
	}
	c.Endpoints.Dispatchers = d
	return nil
}

// indexWebhookSlice processes all webhook contractUsages in a single slice,
// validating each and recording the owning cell ID in the index.
func indexWebhookSlice(sl *SliceMeta, contracts map[string]*ContractMeta, idx *webhookCellIndex) error {
	for _, cu := range sl.ContractUsages {
		if cu.Role != string(cellvocab.RoleWebhookReceive) && cu.Role != string(cellvocab.RoleWebhookDispatch) {
			continue
		}
		c, ok := contracts[cu.Contract]
		if !ok || c.Kind != "webhook" {
			continue
		}
		if err := applyWebhookCU(cu, sl, c, idx); err != nil {
			return err
		}
	}
	return nil
}

// applyWebhookCU dispatches a single webhook ContractUsage to the appropriate
// validator based on role (webhook-receive or webhook-dispatch).
func applyWebhookCU(cu ContractUsage, sl *SliceMeta, c *ContractMeta, idx *webhookCellIndex) error {
	if cu.Role == string(cellvocab.RoleWebhookReceive) {
		return validateWebhookReceive(cu, sl, c, idx)
	}
	return validateWebhookDispatch(cu, sl, c, idx)
}

// validateProjectionUniqueness checks that within a single cell no two
// contractUsages (across any slices) share the same projection id. A slice may
// declare multiple subscribe CUs, but each projection id must be unique within
// the (cellID, projectionID) namespace — two CUs with the same id in the same
// cell would produce ambiguous RegisterProjection registrations at runtime.
//
// Placement is fail-closed (delegated to checkProjectionCUPlacement): a
// non-subscribe CU carrying projection/onReset is rejected (F1), as is group on
// a projection CU (F2) and onReset without a sibling projection (coupling rule).
//
// Uniqueness recording (the (cellID, projectionID) dedup) is skipped only for
// CUs that declare no projection to record — i.e. role != "subscribe", or a
// subscribe CU with an empty projection field. Those are valid, not errors.
func validateProjectionUniqueness(pm *ProjectMeta) error {
	// cellID → projectionID → first sliceID that claimed it.
	seen := make(map[string]map[string]string)
	for _, sl := range pm.Slices {
		if err := checkSliceProjections(sl, seen); err != nil {
			return err
		}
	}
	return nil
}

const (
	msgDuplicateProjection = "duplicate projection id within cell"
	msgOnResetWithoutProj  = "onReset requires projection on the same subscribe contractUsage"
	// F1: projection/onReset are subscribe-only placement columns.
	msgProjectionNonSubscribe = "projection and onReset are only valid on a role=subscribe contractUsage"
	// F2: group: is dead config on a projection CU; cellgen derives the
	// consumer group from cellID+projectionID, not from group:.
	msgGroupOnProjectionCU = "group is not allowed on a projection contractUsage" +
		" (the projection consumer group is derived from cellID and projectionID)"
)

// checkSliceProjections validates all CUs in a single slice for projection
// coupling, uniqueness within the cell, and placement correctness.
// seen is updated in-place (keyed by cellID → projectionID → first sliceID).
func checkSliceProjections(sl *SliceMeta, seen map[string]map[string]string) error {
	for _, cu := range sl.ContractUsages {
		if err := checkProjectionCUPlacement(sl, cu); err != nil {
			return err
		}
		if cu.Role != "subscribe" || cu.Projection == "" {
			continue
		}
		if err := recordProjectionSeen(sl, cu, seen); err != nil {
			return err
		}
	}
	return nil
}

// checkProjectionCUPlacement checks placement rules for a single CU:
// F1 (non-subscribe carrying projection/onReset), onReset-without-projection,
// and F2 (group forbidden on projection CU).
func checkProjectionCUPlacement(sl *SliceMeta, cu ContractUsage) error {
	// F1: fail-closed before the role-guard so non-subscribe CUs carrying
	// projection/onReset are rejected rather than silently skipped.
	if cu.Role != "subscribe" && (cu.Projection != "" || cu.OnReset != "") {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			msgProjectionNonSubscribe,
			errcode.WithDetails(
				errcode.PublicString("slice", sl.ID),
				errcode.PublicString("contract", cu.Contract),
				errcode.PublicString("role", cu.Role),
			))
	}
	if cu.Role != "subscribe" {
		return nil
	}
	if cu.OnReset != "" && cu.Projection == "" {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			msgOnResetWithoutProj,
			errcode.WithDetails(
				errcode.PublicString("slice", sl.ID),
				errcode.PublicString("contract", cu.Contract),
				errcode.PublicString("onReset", cu.OnReset),
			))
	}
	// F2: group is forbidden on a projection CU.
	if cu.Projection != "" && cu.Group != "" {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			msgGroupOnProjectionCU,
			errcode.WithDetails(
				errcode.PublicString("slice", sl.ID),
				errcode.PublicString("contract", cu.Contract),
				errcode.PublicString("projection", cu.Projection),
				errcode.PublicString("group", cu.Group),
			))
	}
	return nil
}

// recordProjectionSeen registers a projection CU in the seen map and returns
// a KindConflict error if the projection id was already claimed by another
// slice in the same cell. F9: the error includes the contract id.
func recordProjectionSeen(sl *SliceMeta, cu ContractUsage, seen map[string]map[string]string) error {
	cellID := sl.BelongsToCell
	if seen[cellID] == nil {
		seen[cellID] = make(map[string]string)
	}
	if firstSlice, dup := seen[cellID][cu.Projection]; dup {
		return errcode.New(errcode.KindConflict, errcode.ErrConflict,
			msgDuplicateProjection,
			errcode.WithDetails(
				errcode.PublicString("cellID", cellID),
				errcode.PublicString("projectionID", cu.Projection),
				errcode.PublicString("contract", cu.Contract),
				errcode.PublicString("firstSliceID", firstSlice),
				errcode.PublicString("conflictSliceID", sl.ID),
			))
	}
	seen[cellID][cu.Projection] = sl.ID
	return nil
}

// dedupSorted returns a new sorted slice with duplicate strings removed.
func dedupSorted(in []string) []string {
	if len(in) == 0 {
		return in
	}
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if _, ok := seen[s]; !ok {
			seen[s] = struct{}{}
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}
