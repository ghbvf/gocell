package main

import (
	"fmt"

	"github.com/ghbvf/gocell/kernel/assembly"
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/runtime/bootstrap"
)

// buildAssembly constructs the runtime Assembly and registers the generated
// cell list. Extracted to keep runCorebundle cognitive complexity <= 15.
func buildAssembly(
	ps promStack,
	assemblyID string,
	mode cell.DurabilityMode,
	clk clock.Clock,
	cells ...cell.Cell,
) (*assembly.CoreAssembly, error) {
	asm := assembly.New(assembly.Config{
		ID:              assemblyID,
		DurabilityMode:  mode,
		Clock:           clk,
		HookObserver:    ps.hookObserver,
		MetricsProvider: ps.metricProvider,
		// HookTimeout omitted → assembly.DefaultHookTimeout (30s) applies.
	})
	for _, c := range cells {
		if err := asm.Register(c); err != nil {
			return nil, fmt.Errorf("register %s: %w", c.ID(), err)
		}
	}
	return asm, nil
}

func durabilityModeForTopology(topo bootstrap.Topology) cell.DurabilityMode {
	if topo.StorageBackend == "postgres" {
		return cell.DurabilityDurable
	}
	return cell.DurabilityDemo
}

// buildConsumerBase constructs ConsumerBase from the topology-selected
// idempotency claimer built in LoadSharedDepsFromEnv.
func buildConsumerBase(deps *SharedDeps) (*outbox.ConsumerBase, error) {
	if deps == nil {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"construct ConsumerBase: SharedDeps is nil")
	}
	if deps.ConsumerClaimer == nil {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"construct ConsumerBase: SharedDeps.ConsumerClaimer must be set")
	}
	cb, err := outbox.NewConsumerBase(deps.ConsumerClaimer, outbox.ConsumerBaseConfig{}, deps.Clock)
	if err != nil {
		return nil, fmt.Errorf("construct ConsumerBase: %w", err)
	}
	return cb, nil
}
