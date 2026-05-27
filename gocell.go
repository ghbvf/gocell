// Package gocell provides the top-level entry point for the GoCell framework.
package gocell

import (
	"github.com/ghbvf/gocell/kernel/assembly"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/outbox"
)

// NewAssembly creates a new CoreAssembly with the given identifier.
func NewAssembly(id string) *assembly.CoreAssembly {
	return assembly.New(clock.Real(), assembly.Config{ID: id, DurabilityMode: outbox.DurabilityDemo})
}
