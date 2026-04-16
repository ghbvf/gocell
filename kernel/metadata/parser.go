package metadata

import (
	"bytes"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/ghbvf/gocell/pkg/errcode"
	"gopkg.in/yaml.v3"
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
		Nodes:      make(map[string]*yaml.Node),
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

// matchCellYAML matches paths like cells/*/cell.yaml (exactly 3 segments).
func matchCellYAML(path string) bool {
	parts := splitPath(path)
	return len(parts) == 3 && parts[0] == "cells" && parts[2] == "cell.yaml"
}

// matchSliceYAML matches paths like cells/*/slices/*/slice.yaml (exactly 5 segments).
func matchSliceYAML(path string) bool {
	parts := splitPath(path)
	return len(parts) == 5 && parts[0] == "cells" && parts[2] == "slices" && parts[4] == "slice.yaml"
}

// matchContractYAML matches paths like contracts/{kind}/{...}/{version}/contract.yaml.
// The path must start with "contracts/" and end with "contract.yaml", with at least
// 4 segments between (kind + at least one domain segment + version).
func matchContractYAML(path string) bool {
	parts := splitPath(path)
	if len(parts) < 5 {
		return false
	}
	return parts[0] == "contracts" && parts[len(parts)-1] == "contract.yaml"
}

// matchJourneyYAML matches paths like journeys/J-*.yaml (exactly 2 segments).
func matchJourneyYAML(path string) bool {
	parts := splitPath(path)
	if len(parts) != 2 || parts[0] != "journeys" {
		return false
	}
	name := parts[1]
	return strings.HasPrefix(name, "J-") && strings.HasSuffix(name, ".yaml")
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

// --- individual parsers ---

func (p *Parser) parseCell(fsys fs.FS, path string, pm *ProjectMeta) error {
	var m CellMeta
	node, err := unmarshalFile(fsys, path, &m)
	if err != nil {
		return err
	}
	if node != nil {
		pm.Nodes[path] = node
	}
	if m.ID == "" {
		return errcode.New(errcode.ErrMetadataInvalid,
			fmt.Sprintf("cell id is empty in %s", path))
	}
	if _, exists := pm.Cells[m.ID]; exists {
		return errcode.New(errcode.ErrMetadataInvalid,
			fmt.Sprintf("duplicate cell ID %q: %s and previous", m.ID, path))
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
	if node != nil {
		pm.Nodes[path] = node
	}
	if m.ID == "" {
		return errcode.New(errcode.ErrMetadataInvalid,
			fmt.Sprintf("slice id is empty in %s", path))
	}

	// G-7: auto-derive belongsToCell from path.
	// matchSliceYAML guarantees len(parts)==5 && parts[0]=="cells",
	// so parts[1] is always the cellID.
	parts := splitPath(path)
	cellID := parts[1]

	if m.BelongsToCell == "" {
		m.BelongsToCell = cellID
	} else if m.BelongsToCell != cellID {
		return errcode.New(errcode.ErrMetadataInvalid,
			fmt.Sprintf("slice %q: belongsToCell %q does not match directory cell %q in %s",
				m.ID, m.BelongsToCell, cellID, path))
	}

	key := cellID + "/" + m.ID
	if _, exists := pm.Slices[key]; exists {
		return errcode.New(errcode.ErrMetadataInvalid,
			fmt.Sprintf("duplicate slice ID %q: %s and previous", key, path))
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
	if node != nil {
		pm.Nodes[path] = node
	}
	if m.ID == "" {
		return errcode.New(errcode.ErrMetadataInvalid,
			fmt.Sprintf("contract id is empty in %s", path))
	}
	// G-7: auto-derive ownerCell from provider endpoint if omitted (per contract.schema.json).
	if m.OwnerCell == "" {
		m.OwnerCell = m.ProviderEndpoint()
	}

	if _, exists := pm.Contracts[m.ID]; exists {
		return errcode.New(errcode.ErrMetadataInvalid,
			fmt.Sprintf("duplicate contract ID %q: %s and previous", m.ID, path))
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
	if node != nil {
		pm.Nodes[path] = node
	}
	if m.ID == "" {
		return errcode.New(errcode.ErrMetadataInvalid,
			fmt.Sprintf("journey id is empty in %s", path))
	}
	if _, exists := pm.Journeys[m.ID]; exists {
		return errcode.New(errcode.ErrMetadataInvalid,
			fmt.Sprintf("duplicate journey ID %q: %s and previous", m.ID, path))
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
	if node != nil {
		pm.Nodes[path] = node
	}
	if m.ID == "" {
		return errcode.New(errcode.ErrMetadataInvalid,
			fmt.Sprintf("assembly id is empty in %s", path))
	}
	if _, exists := pm.Assemblies[m.ID]; exists {
		return errcode.New(errcode.ErrMetadataInvalid,
			fmt.Sprintf("duplicate assembly ID %q: %s and previous", m.ID, path))
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
	if node != nil {
		pm.Nodes[path] = node
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
	if node != nil {
		pm.Nodes[path] = node
	}
	pm.Actors = actors
	return nil
}

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
// Empty / whitespace-only files are treated as "no content" and return
// (nil, nil) to preserve the original behaviour of empty actors.yaml or
// status-board.yaml. Multi-document files are rejected.
func unmarshalFile(fsys fs.FS, path string, out any) (*yaml.Node, error) {
	data, err := fs.ReadFile(fsys, path)
	if err != nil {
		return nil, errcode.Wrap(errcode.ErrMetadataInvalid,
			fmt.Sprintf("read %s", path), err)
	}

	// Phase 1: capture location-preserving AST.
	var root yaml.Node
	dec1 := yaml.NewDecoder(bytes.NewReader(data))
	if err := dec1.Decode(&root); err != nil {
		if err == io.EOF {
			return nil, nil
		}
		return nil, errcode.Wrap(errcode.ErrMetadataInvalid,
			fmt.Sprintf("parse %s", path), err)
	}
	// Reject multi-document YAML files. Metadata files must contain exactly
	// one document; a second document after "---" would be silently ignored
	// by a single Decode call.
	if dec1.Decode(new(yaml.Node)) != io.EOF {
		return nil, errcode.New(errcode.ErrMetadataInvalid,
			fmt.Sprintf("parse %s: unexpected multiple YAML documents", path))
	}

	// Phase 2: strict decode into target struct. This is where KnownFields(true)
	// catches typos and yaml.v3 produces "line N: field X not found in type ..."
	// errors that already carry line numbers.
	dec2 := yaml.NewDecoder(bytes.NewReader(data))
	dec2.KnownFields(true)
	if err := dec2.Decode(out); err != nil {
		if err == io.EOF {
			// Unreachable: phase 1 already saw a document, so phase 2 cannot
			// be empty. Kept defensively to mirror the original behaviour.
			return &root, nil
		}
		return nil, errcode.Wrap(errcode.ErrMetadataInvalid,
			fmt.Sprintf("parse %s", path), err)
	}

	return &root, nil
}
