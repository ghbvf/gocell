//go:build archtest_fixture

// Package rawparamfixture is a deliberate
// CELL-RAW-INFRA-PUBLIC-OPTION-PARAM-01 negative fixture loaded only when the
// archtest_fixture build tag is set.
//
// The build tag excludes this package from `go build ./...` and `go test
// ./...` so it never pollutes real-repo scans. It is loaded explicitly by
// TestCellRawInfraPublicOptionParam01_ScannerCatchesViolation via
//
//	typeseval.SharedResolver(root, false, []string{"archtest_fixture"},
//	    "./tools/archtest/internal/rawparamfixture")
//
// The archtest scans cells/<x>/*.go + examples/<demo>/cells/<x>/*.go for
// exported With* functions whose parameter canonical type is in the
// forbidden raw-infra set. The fixture lives outside those paths, so the
// detection test bypasses the path filter and runs the file scanner
// directly on each fixture file (mirrors the wrapper-location detection
// fixture pattern, ai-collab.md §"real source AST capture").
package rawparamfixture

import (
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/persistence"
)

// Option is a placeholder functional-option type matching the cell.go
// pattern the scanner looks for (exported With* top-level function).
type Option func(any)

// WithBadTxRunner deliberately exposes raw persistence.TxRunner as a public
// Option parameter — CELL-RAW-INFRA-PUBLIC-OPTION-PARAM-01 must report this
// as a violation (cells/<x>/cell.go must accept persistence.CellTxManager
// instead).
func WithBadTxRunner(tx persistence.TxRunner) Option { return func(any) {} }

// WithBadPublisher exposes raw outbox.Publisher; must use outbox.CellPublisher.
func WithBadPublisher(pub outbox.Publisher) Option { return func(any) {} }

// WithBadWriter exposes raw outbox.Writer; must use outbox.CellWriter.
func WithBadWriter(w outbox.Writer) Option { return func(any) {} }

// AliasedTxRunner exercises the types.Unalias bypass: a Go 1.23+ type alias
// to a forbidden raw type. Without types.Unalias in the scanner, the
// canonical name resolves to the local alias and the violation is missed.
type AliasedTxRunner = persistence.TxRunner

// WithAliasedBadTxRunner is the alias-bypass detection fixture — must also
// be reported as a violation because the canonical resolves through Unalias
// to persistence.TxRunner.
func WithAliasedBadTxRunner(tx AliasedTxRunner) Option { return func(any) {} }
