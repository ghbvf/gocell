//go:build archtest_fixture

// Package rawparamfixture is a deliberate
// CELL-RAW-INFRA-PUBLIC-OPTION-PARAM-01 negative fixture loaded only when the
// archtest_fixture build tag is set.
//
// 本 fixture 包含 10 个违规的 With* Option 函数（4 基础 + 3 嵌入形式 + 1 纯方法接口
// + 1 命名本地嵌入 + 1 泛型）。修改本文件请同步更新
// tools/archtest/cell_public_option_param_test.go 的 expectedRawParamFixtureViolations 常量。
//
// The build tag excludes this package from `go build ./...` and `go test
// ./...` so it never pollutes real-repo scans. It is loaded explicitly by
// TestCellRawInfraPublicOptionParam01_ScannerCatchesViolation via
//
//	archtest.RunTypedFixture(t, archtest.FixtureOpts{Tests: false},
//	    []string{"./tools/archtest/internal/rawparamfixture"}, rule)
//
// The archtest scans cells/<x>/*.go + examples/<demo>/cells/<x>/*.go for
// exported With* functions whose parameter canonical type is in the
// forbidden raw-infra set. The fixture lives outside those paths, so the
// detection test bypasses the path filter and runs the file scanner
// directly on each fixture file (mirrors the wrapper-location detection
// fixture pattern, ai-collab.md §"real source AST capture").
package rawparamfixture

import (
	"context"

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

// WithBadEmbedPublisher exposes outbox.Publisher via inline interface
// embedding — Go allows `func(p interface{ outbox.Publisher })` as a
// signature where tv.Type resolves to *types.Interface (anonymous) with
// outbox.Publisher in EmbeddedTypes(). A *types.Named-only check would
// miss this, so the scanner must also walk the embedded types of an
// anonymous interface parameter.
func WithBadEmbedPublisher(p interface{ outbox.Publisher }) Option { return func(any) {} }

// WithBadEmbedWriter mirrors WithBadEmbedPublisher for the Writer leg.
func WithBadEmbedWriter(w interface{ outbox.Writer }) Option { return func(any) {} }

// WithBadEmbedTxRunner mirrors the embed pattern for the TxRunner leg.
func WithBadEmbedTxRunner(tx interface{ persistence.TxRunner }) Option { return func(any) {} }

// WithBadPureMethodIfaceTxRunner is the structurally-equivalent anonymous
// interface bypass: no named embed, only the methods that
// persistence.TxRunner declares. NumEmbeddeds()==0 so the embed-walk
// heuristic returns ""; without the types.Implements fall-through the
// scanner misses this form even though the parameter is assignable from
// any persistence.TxRunner implementer (Go's structural typing).
//
// Method set must match persistence.TxRunner (RunInTx) — the scanner uses
// types.Implements(tv.Type, forbiddenIface) for anonymous interfaces with
// no embedded named types.
func WithBadPureMethodIfaceTxRunner(tx interface {
	RunInTx(ctx context.Context, fn func(context.Context) error) error
},
) Option {
	return func(any) {}
}

// LocalRawPub is a named local interface that embeds outbox.Publisher.
// Without recursive underlying inspection in canonicalFromType, the
// scanner sees `*types.Named` (LocalRawPub) → returns the local canonical
// (`<fixture>.LocalRawPub`) which is not in the forbidden set → violation
// missed.
type LocalRawPub interface {
	outbox.Publisher
}

// WithBadNamedLocalEmbedPublisher exposes outbox.Publisher via a named
// local interface that embeds it. The scanner must recurse into the
// named type's underlying *types.Interface to detect the embedded
// forbidden type.
func WithBadNamedLocalEmbedPublisher(p LocalRawPub) Option { return func(any) {} }

// WithGenericTx is a generic function whose type parameter is constrained to
// persistence.TxRunner. The scanner must walk *types.TypeParam.Constraint()
// to detect this forbidden type. Without the TypeParam case in
// canonicalFromType, this bypasses the guard.
func WithGenericTx[T persistence.TxRunner](tx T) Option { return func(any) {} }
