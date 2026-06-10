//go:build archtest_fixture

// Package flagwrite is a minimal fixture for requireddepsgen golden tests.
package flagwrite

import (
	"github.com/ghbvf/gocell/corecells/configcore/internal/ports"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/persistence"
)

// Service holds the dependencies for the flagwrite slice.
type Service struct {
	repo     ports.FlagRepository      `gocell:"required"`
	txRunner persistence.CellTxManager `gocell:"required"`
	clock    clock.Clock
}
