// Package gocell provides the top-level entry point for the GoCell framework.
package gocell

import (
	"github.com/ghbvf/gocell/kernel/assembly"
	"github.com/ghbvf/gocell/kernel/cell"
)

// NewAssembly creates a new CoreAssembly with the given identifier.
func NewAssembly(id string) *assembly.CoreAssembly {
	return assembly.New(assembly.Config{ID: id, DurabilityMode: cell.DurabilityDemo})
}
