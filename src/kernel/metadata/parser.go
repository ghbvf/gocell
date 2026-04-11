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
// The root should point to the project's src/ directory (the metadata root).
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
	if err := unmarshalFile(fsys, path, &m); err != nil {
		return err
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
	if err := unmarshalFile(fsys, path, &m); err != nil {
		return err
	}
	if m.ID == "" {
		return errcode.New(errcode.ErrMetadataInvalid,
			fmt.Sprintf("slice id is empty in %s", path))
	}

	// G-7: auto-derive belongsToCell from path.
	// Path is guaranteed to be cells/{cellID}/slices/{sliceID}/slice.yaml by matchSliceYAML.
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
	if err := unmarshalFile(fsys, path, &m); err != nil {
		return err
	}
	if m.ID == "" {
		return errcode.New(errcode.ErrMetadataInvalid,
			fmt.Sprintf("contract id is empty in %s", path))
	}
	// G-7: auto-derive ownerCell from provider endpoint if omitted.
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
	if err := unmarshalFile(fsys, path, &m); err != nil {
		return err
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
	if err := unmarshalFile(fsys, path, &m); err != nil {
		return err
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
	if err := unmarshalFile(fsys, path, &entries); err != nil {
		return err
	}
	pm.StatusBoard = entries
	return nil
}

func (p *Parser) parseActors(fsys fs.FS, path string, pm *ProjectMeta) error {
	var actors []ActorMeta
	if err := unmarshalFile(fsys, path, &actors); err != nil {
		return err
	}
	pm.Actors = actors
	return nil
}

// unmarshalFile reads and decodes a YAML file from fsys with strict field
// checking. Unknown YAML keys that don't map to struct fields are rejected,
// preventing silent typos in metadata files (e.g., "ownerId" instead of "ownerCell").
func unmarshalFile(fsys fs.FS, path string, out any) error {
	data, err := fs.ReadFile(fsys, path)
	if err != nil {
		return errcode.Wrap(errcode.ErrMetadataInvalid,
			fmt.Sprintf("read %s", path), err)
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(out); err != nil {
		// yaml.Decoder returns io.EOF for empty/whitespace-only files, whereas
		// yaml.Unmarshal silently leaves the target at its zero value. Preserve
		// the old behaviour so that empty actors.yaml / status-board.yaml parse
		// as empty slices instead of aborting.
		if err == io.EOF {
			return nil
		}
		return errcode.Wrap(errcode.ErrMetadataInvalid,
			fmt.Sprintf("parse %s", path), err)
	}
	// Reject multi-document YAML files. Metadata files must contain exactly
	// one document; a second document after "---" would be silently ignored
	// by a single Decode call.
	if dec.Decode(new(any)) != io.EOF {
		return errcode.New(errcode.ErrMetadataInvalid,
			fmt.Sprintf("parse %s: unexpected multiple YAML documents", path))
	}
	return nil
}
