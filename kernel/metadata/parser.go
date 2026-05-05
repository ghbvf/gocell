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
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/ghbvf/gocell/pkg/errcode"
)

const (
	internalPathFmt         = "path=%s"
	internalIDPathQuotedFmt = "id=%q path=%s"
)

// Parser loads and parses all YAML metadata from a project root.
type Parser struct {
	root string
}

// NewParser creates a Parser that reads from the given filesystem root.
// The root should point to the project root directory (containing go.mod).
func NewParser(root string) *Parser {
	return &Parser{root: root}
}

// Parse walks the real file system and loads all metadata YAML files.
// Returns a fully populated ProjectMeta.
func (p *Parser) Parse() (*ProjectMeta, error) {
	return p.ParseFS(os.DirFS(p.root))
}

// ParseFS parses from an fs.FS (for testing with fstest.MapFS).
func (p *Parser) ParseFS(fsys fs.FS) (*ProjectMeta, error) {
	pm := &ProjectMeta{
		Cells:      make(map[string]*CellMeta),
		Slices:     make(map[string]*SliceMeta),
		Contracts:  make(map[string]*ContractMeta),
		Journeys:   make(map[string]*JourneyMeta),
		Assemblies: make(map[string]*AssemblyMeta),
		fileNodes:  make(map[string]*yaml.Node),
	}

	if err := fs.WalkDir(fsys, ".", func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}

		switch {
		case matchCellYAML(path):
			return p.parseCell(fsys, path, pm)
		case matchSliceYAML(path):
			return p.parseSlice(fsys, path, pm)
		case matchContractYAML(path):
			return p.parseContract(fsys, path, pm)
		case matchJourneyYAML(path):
			return p.parseJourney(fsys, path, pm)
		case matchAssemblyYAML(path):
			return p.parseAssembly(fsys, path, pm)
		case path == "journeys/status-board.yaml":
			return p.parseStatusBoard(fsys, path, pm)
		case path == "actors.yaml":
			return p.parseActors(fsys, path, pm)
		}
		return nil
	}); err != nil {
		return nil, err
	}

	return pm, nil
}

// matchCellYAML matches cells/*/cell.yaml and examples/*/cells/*/cell.yaml.
func matchCellYAML(path string) bool {
	_, ok := cellDirFromPath(path)
	return ok
}

// matchSliceYAML matches cells/*/slices/*/slice.yaml and
// examples/*/cells/*/slices/*/slice.yaml.
func matchSliceYAML(path string) bool {
	_, _, ok := sliceDirsFromPath(path)
	return ok
}

// matchContractYAML matches paths like contracts/{kind}/{...}/{version}/contract.yaml
// and examples/*/contracts/{kind}/{...}/{version}/contract.yaml.
// The path must start with "contracts/" and end with "contract.yaml", with at least
// 4 segments between (kind + at least one domain segment + version).
func matchContractYAML(path string) bool {
	_, ok := contractDirFromPath(path)
	return ok
}

// matchJourneyYAML matches journeys/J-*.yaml and examples/*/journeys/J-*.yaml.
func matchJourneyYAML(path string) bool {
	_, ok := journeyIDFromPath(path)
	return ok
}

// matchAssemblyYAML matches paths like assemblies/*/assembly.yaml (exactly 3 segments).
func matchAssemblyYAML(path string) bool {
	parts := splitPath(path)
	return len(parts) == 3 && parts[0] == "assemblies" && parts[2] == "assembly.yaml"
}

// splitPath splits a forward-slash-separated path into its segments.
func splitPath(path string) []string {
	// Normalise to forward slashes for consistent matching.
	clean := filepath.ToSlash(path)
	return strings.Split(clean, "/")
}

func cellDirFromPath(path string) (string, bool) {
	parts := splitPath(path)
	if len(parts) == 3 && parts[0] == "cells" && parts[2] == "cell.yaml" {
		return parts[1], true
	}
	if len(parts) == 5 && parts[0] == "examples" && parts[2] == "cells" && parts[4] == "cell.yaml" {
		return parts[3], true
	}
	return "", false
}

func sliceDirsFromPath(path string) (cellDir, sliceDir string, ok bool) {
	parts := splitPath(path)
	if len(parts) == 5 && parts[0] == "cells" && parts[2] == "slices" && parts[4] == "slice.yaml" {
		return parts[1], parts[3], true
	}
	if len(parts) == 7 && parts[0] == "examples" && parts[2] == "cells" && parts[4] == "slices" && parts[6] == "slice.yaml" {
		return parts[3], parts[5], true
	}
	return "", "", false
}

func contractDirFromPath(path string) (string, bool) {
	parts := splitPath(path)
	if len(parts) >= 5 && parts[0] == "contracts" && parts[len(parts)-1] == "contract.yaml" {
		return strings.Join(parts[:len(parts)-1], "/"), true
	}
	if len(parts) >= 7 && parts[0] == "examples" && parts[2] == "contracts" && parts[len(parts)-1] == "contract.yaml" {
		return strings.Join(parts[:len(parts)-1], "/"), true
	}
	return "", false
}

func journeyIDFromPath(path string) (string, bool) {
	parts := splitPath(path)
	var name string
	switch {
	case len(parts) == 2 && parts[0] == "journeys":
		name = parts[1]
	case len(parts) == 4 && parts[0] == "examples" && parts[2] == "journeys":
		name = parts[3]
	default:
		return "", false
	}
	if !strings.HasPrefix(name, "J-") || !strings.HasSuffix(name, ".yaml") {
		return "", false
	}
	return strings.TrimSuffix(name, ".yaml"), true
}

// --- individual parsers ---

func (p *Parser) parseCell(fsys fs.FS, path string, pm *ProjectMeta) error {
	var m CellMeta
	node, err := unmarshalFile(fsys, path, &m)
	if err != nil {
		return err
	}
	if shouldCacheFileNode(node) {
		pm.fileNodes[path] = node
	}
	if m.ID == "" {
		return errcode.New(errcode.KindInvalid, errcode.ErrMetadataInvalid,
			"cell id is empty",
			errcode.WithInternal(fmt.Sprintf(internalPathFmt, path)))
	}
	// Record the real filesystem directory so strict rules (REF-04) can
	// compare it against m.ID instead of self-comparing against the map key.
	// Use ToSlash so that on Windows (where os.DirFS produces backslash paths)
	// all metadata file paths are normalised to forward slashes — making
	// validation error messages cross-platform consistent.
	cellDir, _ := cellDirFromPath(path)
	m.Dir = cellDir
	m.File = filepath.ToSlash(path)
	if _, exists := pm.Cells[m.ID]; exists {
		return errcode.New(errcode.KindInvalid, errcode.ErrMetadataInvalid,
			"duplicate cell ID",
			errcode.WithInternal(fmt.Sprintf(internalIDPathQuotedFmt, m.ID, path)))
	}
	pm.Cells[m.ID] = &m
	return nil
}

// parseSlice parses a slice.yaml and applies G-7 auto-derivation:
// if belongsToCell is omitted, it is inferred from the file path
// (cells/{cellID}/slices/{sliceID}/slice.yaml → belongsToCell = cellID).
// If an explicit value is provided but mismatches the path, an error is returned.
func (p *Parser) parseSlice(fsys fs.FS, path string, pm *ProjectMeta) error {
	var m SliceMeta
	node, err := unmarshalFile(fsys, path, &m)
	if err != nil {
		return err
	}
	if shouldCacheFileNode(node) {
		pm.fileNodes[path] = node
	}
	if m.ID == "" {
		return errcode.New(errcode.KindInvalid, errcode.ErrMetadataInvalid,
			"slice id is empty",
			errcode.WithInternal(fmt.Sprintf(internalPathFmt, path)))
	}

	// G-7: auto-derive belongsToCell from path.
	cellID, sliceDir, _ := sliceDirsFromPath(path)

	if m.BelongsToCell == "" {
		m.BelongsToCell = cellID
	} else if m.BelongsToCell != cellID {
		return errcode.New(errcode.KindInvalid, errcode.ErrMetadataInvalid,
			"slice belongsToCell does not match directory cell",
			errcode.WithInternal(fmt.Sprintf("slice=%q belongs_to=%q dir_cell=%q path=%s",
				m.ID, m.BelongsToCell, cellID, path)))
	}

	// Record filesystem truth separately from the yaml id. Strict rules
	// (FMT-16, FMT-17, REF-05) consume these fields so a path-vs-id split
	// (kebab dir paired with no-dash id, or vice versa) cannot escape the
	// governance gate. ToSlash normalises Windows backslashes so error
	// messages are cross-platform consistent.
	m.Dir = sliceDir
	m.CellDir = cellID
	m.File = filepath.ToSlash(path)

	key := cellID + "/" + m.ID
	if _, exists := pm.Slices[key]; exists {
		return errcode.New(errcode.KindInvalid, errcode.ErrMetadataInvalid,
			"duplicate slice ID",
			errcode.WithInternal(fmt.Sprintf(internalIDPathQuotedFmt, key, path)))
	}
	pm.Slices[key] = &m
	return nil
}

// parseContract parses a contract.yaml and applies G-7 auto-derivation:
// if ownerCell is omitted, it is inferred from the provider endpoint based on
// the contract kind (http→server, event→publisher, command→handler,
// projection→provider). If the provider endpoint is also empty, ownerCell
// remains empty and governance rules will flag the issue.
func (p *Parser) parseContract(fsys fs.FS, path string, pm *ProjectMeta) error {
	var m ContractMeta
	node, err := unmarshalFile(fsys, path, &m)
	if err != nil {
		return err
	}
	if shouldCacheFileNode(node) {
		pm.fileNodes[path] = node
	}
	if m.ID == "" {
		return errcode.New(errcode.KindInvalid, errcode.ErrMetadataInvalid,
			"contract id is empty",
			errcode.WithInternal(fmt.Sprintf(internalPathFmt, path)))
	}
	// G-7: auto-derive ownerCell from provider endpoint if omitted (per contract.schema.json).
	if m.OwnerCell == "" {
		m.OwnerCell = m.ProviderEndpoint()
	}
	m.Dir, _ = contractDirFromPath(path)
	m.File = filepath.ToSlash(path)

	if _, exists := pm.Contracts[m.ID]; exists {
		return errcode.New(errcode.KindInvalid, errcode.ErrMetadataInvalid,
			"duplicate contract ID",
			errcode.WithInternal(fmt.Sprintf(internalIDPathQuotedFmt, m.ID, path)))
	}
	pm.Contracts[m.ID] = &m
	return nil
}

func (p *Parser) parseJourney(fsys fs.FS, path string, pm *ProjectMeta) error {
	var m JourneyMeta
	node, err := unmarshalFile(fsys, path, &m)
	if err != nil {
		return err
	}
	if shouldCacheFileNode(node) {
		pm.fileNodes[path] = node
	}
	if m.ID == "" {
		return errcode.New(errcode.KindInvalid, errcode.ErrMetadataInvalid,
			"journey id is empty",
			errcode.WithInternal(fmt.Sprintf(internalPathFmt, path)))
	}
	m.File = filepath.ToSlash(path)
	if _, exists := pm.Journeys[m.ID]; exists {
		return errcode.New(errcode.KindInvalid, errcode.ErrMetadataInvalid,
			"duplicate journey ID",
			errcode.WithInternal(fmt.Sprintf(internalIDPathQuotedFmt, m.ID, path)))
	}
	pm.Journeys[m.ID] = &m
	return nil
}

func (p *Parser) parseAssembly(fsys fs.FS, path string, pm *ProjectMeta) error {
	var m AssemblyMeta
	node, err := unmarshalFile(fsys, path, &m)
	if err != nil {
		return err
	}
	if shouldCacheFileNode(node) {
		pm.fileNodes[path] = node
	}
	if m.ID == "" {
		return errcode.New(errcode.KindInvalid, errcode.ErrMetadataInvalid,
			"assembly id is empty",
			errcode.WithInternal(fmt.Sprintf(internalPathFmt, path)))
	}
	// Record filesystem truth so strict rules (FMT-16) can compare the directory
	// segment against m.ID. matchAssemblyYAML guarantees len(parts)==3 and
	// parts[0]=="assemblies", so parts[1] is always the assembly directory.
	parts := splitPath(path)
	m.Dir = parts[1]
	m.File = filepath.ToSlash(path)
	if _, exists := pm.Assemblies[m.ID]; exists {
		return errcode.New(errcode.KindInvalid, errcode.ErrMetadataInvalid,
			"duplicate assembly ID",
			errcode.WithInternal(fmt.Sprintf(internalIDPathQuotedFmt, m.ID, path)))
	}
	pm.Assemblies[m.ID] = &m
	return nil
}

func (p *Parser) parseStatusBoard(fsys fs.FS, path string, pm *ProjectMeta) error {
	var entries []StatusBoardEntry
	node, err := unmarshalFile(fsys, path, &entries)
	if err != nil {
		return err
	}
	if shouldCacheFileNode(node) {
		pm.fileNodes[path] = node
	}
	pm.StatusBoard = entries
	return nil
}

func (p *Parser) parseActors(fsys fs.FS, path string, pm *ProjectMeta) error {
	var actors []ActorMeta
	node, err := unmarshalFile(fsys, path, &actors)
	if err != nil {
		return err
	}
	if shouldCacheFileNode(node) {
		pm.fileNodes[path] = node
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
			errcode.WithInternal(fmt.Sprintf(internalPathFmt, path)))
	}
	if len(data) > maxMetadataFileSize {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrMetadataInvalid,
			"metadata file exceeds size limit",
			errcode.WithInternal(fmt.Sprintf("path=%s size=%d limit=%d", path, len(data), maxMetadataFileSize)))
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
			errcode.WithInternal(fmt.Sprintf(internalPathFmt, path)))
	}
	// Reject multi-document YAML files. Metadata files must contain exactly
	// one document; a second document after "---" would be silently ignored
	// by a single Decode call.
	tmpErr := dec1.Decode(new(yaml.Node))
	if !errors.Is(tmpErr, io.EOF) {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrMetadataInvalid,
			"unexpected multiple YAML documents in metadata file",
			errcode.WithInternal(fmt.Sprintf(internalPathFmt, path)))
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
			errcode.WithInternal(fmt.Sprintf(internalPathFmt, path)))
	}

	return &root, nil
}

func emptyYAMLDocumentNode() *yaml.Node {
	return &yaml.Node{Kind: yaml.DocumentNode}
}

func shouldCacheFileNode(node *yaml.Node) bool {
	return node != nil && (node.Kind != yaml.DocumentNode || len(node.Content) != 0)
}
