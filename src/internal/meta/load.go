package meta

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

func LoadRepository(root string) (*Repository, error) {
	repo := &Repository{
		Root:   root,
		Actors: map[string]Actor{},
	}

	if err := loadActors(repo); err != nil {
		return nil, err
	}
	if err := loadAssemblies(repo); err != nil {
		return nil, err
	}
	if err := loadCells(repo); err != nil {
		return nil, err
	}
	if err := loadSlices(repo); err != nil {
		return nil, err
	}
	if err := loadContracts(repo); err != nil {
		return nil, err
	}
	if err := loadJourneys(repo); err != nil {
		return nil, err
	}
	if err := loadStatusBoard(repo); err != nil {
		return nil, err
	}

	return repo, nil
}

func loadActors(repo *Repository) error {
	path := filepath.Join(repo.Root, "actors.yaml")
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil
	}

	var actors []Actor
	if err := decodeYAMLFile(path, &actors); err != nil {
		return err
	}
	for _, actor := range actors {
		repo.Actors[actor.ID] = actor
	}
	return nil
}

func loadAssemblies(repo *Repository) error {
	paths, err := filepath.Glob(filepath.Join(repo.Root, "assemblies", "*", "assembly.yaml"))
	if err != nil {
		return err
	}
	for _, path := range paths {
		var assembly Assembly
		if err := decodeYAMLFile(path, &assembly); err != nil {
			return err
		}
		repo.Assemblies = append(repo.Assemblies, &AssemblyFile{
			Path:     path,
			DirID:    filepath.Base(filepath.Dir(path)),
			Assembly: assembly,
		})
	}
	return nil
}

func loadCells(repo *Repository) error {
	paths, err := filepath.Glob(filepath.Join(repo.Root, "cells", "*", "cell.yaml"))
	if err != nil {
		return err
	}
	for _, path := range paths {
		var cell Cell
		if err := decodeYAMLFile(path, &cell); err != nil {
			return err
		}
		repo.Cells = append(repo.Cells, &CellFile{
			Path:  path,
			DirID: filepath.Base(filepath.Dir(path)),
			Cell:  cell,
		})
	}
	return nil
}

func loadSlices(repo *Repository) error {
	paths, err := filepath.Glob(filepath.Join(repo.Root, "cells", "*", "slices", "*", "slice.yaml"))
	if err != nil {
		return err
	}
	for _, path := range paths {
		var slice Slice
		if err := decodeYAMLFile(path, &slice); err != nil {
			return err
		}
		repo.Slices = append(repo.Slices, &SliceFile{
			Path:          path,
			DirID:         filepath.Base(filepath.Dir(path)),
			ParentCellDir: filepath.Base(filepath.Dir(filepath.Dir(filepath.Dir(path)))),
			Slice:         slice,
		})
	}
	return nil
}

func loadContracts(repo *Repository) error {
	contractsRoot := filepath.Join(repo.Root, "contracts")
	return filepath.WalkDir(contractsRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || d.Name() != "contract.yaml" {
			return nil
		}

		var contract Contract
		if err := decodeYAMLFile(path, &contract); err != nil {
			return err
		}
		relDir, err := filepath.Rel(contractsRoot, filepath.Dir(path))
		if err != nil {
			return err
		}
		parts := splitPath(relDir)
		kindDir := ""
		versionDir := ""
		if len(parts) > 0 {
			kindDir = parts[0]
			versionDir = parts[len(parts)-1]
		}
		repo.Contracts = append(repo.Contracts, &ContractFile{
			Path:       path,
			KindDir:    kindDir,
			VersionDir: versionDir,
			Contract:   contract,
		})
		return nil
	})
}

func loadJourneys(repo *Repository) error {
	paths, err := filepath.Glob(filepath.Join(repo.Root, "journeys", "*.yaml"))
	if err != nil {
		return err
	}
	for _, path := range paths {
		if filepath.Base(path) == "status-board.yaml" {
			continue
		}
		var journey Journey
		if err := decodeYAMLFile(path, &journey); err != nil {
			return err
		}
		repo.Journeys = append(repo.Journeys, &JourneyFile{
			Path:    path,
			Journey: journey,
		})
	}
	return nil
}

func loadStatusBoard(repo *Repository) error {
	path := filepath.Join(repo.Root, "journeys", "status-board.yaml")
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil
	}

	var entries []StatusEntry
	if err := decodeYAMLFile(path, &entries); err != nil {
		return err
	}
	repo.Status = &StatusBoardFile{
		Path:    path,
		Entries: entries,
	}
	return nil
}

func decodeYAMLFile(path string, out any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	if err := yaml.Unmarshal(data, out); err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}
	return nil
}

func splitPath(path string) []string {
	if path == "" || path == "." {
		return nil
	}
	return splitAndFilter(path, string(filepath.Separator))
}

func splitAndFilter(s, sep string) []string {
	raw := strings.Split(s, sep)
	out := make([]string, 0, len(raw))
	for _, part := range raw {
		if part == "" || part == "." {
			continue
		}
		out = append(out, part)
	}
	return out
}
