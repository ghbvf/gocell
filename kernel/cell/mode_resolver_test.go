package cell_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/pkg/errcode"
)

// --- local stubs (defined only in this test file) ---

// fakeWriter is a real (non-noop) outbox.Writer for test use.
type fakeWriter struct{}

func (fakeWriter) Write(_ context.Context, _ outbox.Entry) error { return nil }

// fakeTxRunner is a real (non-noop) persistence.TxRunner for test use.
type fakeTxRunner struct{}

func (fakeTxRunner) RunInTx(_ context.Context, fn func(context.Context) error) error {
	return fn(context.Background())
}

// fakePublisher is a real (non-noop) outbox.Publisher for test use.
type fakePublisher struct{}

func (fakePublisher) Publish(_ context.Context, _ string, _ []byte) error { return nil }
func (fakePublisher) Close(_ context.Context) error                       { return nil }

// TestResolveEmitter covers the 10-case table described in the PR-A5a spec.
func TestResolveEmitter(t *testing.T) {
	t.Parallel()

	noopWriter := outbox.NoopWriter{}
	noopTx := persistence.NoopTxRunner{}
	noopPub := &outbox.DiscardPublisher{}
	realW := fakeWriter{}
	realTx := fakeTxRunner{}
	realPub := fakePublisher{}

	tests := []struct {
		name        string
		cfg         cell.EmitterConfig
		wantDurable bool
		wantErr     bool
	}{
		{
			// A: durable + full real deps → WriterEmitter, durable=true
			name: "A_durable_full",
			cfg: cell.EmitterConfig{
				CellID:          "testcell",
				Mode:            cell.DurabilityDurable,
				Publisher:       realPub,
				OutboxWriter:    realW,
				TxRunner:        realTx,
				MetricsProvider: metrics.NopProvider{},
			},
			wantDurable: true,
		},
		{
			// B: durable + missing writer → error (ErrCellMissingOutbox)
			name: "B_durable_missing_writer",
			cfg: cell.EmitterConfig{
				CellID:          "testcell",
				Mode:            cell.DurabilityDurable,
				Publisher:       realPub,
				OutboxWriter:    nil,
				TxRunner:        realTx,
				MetricsProvider: metrics.NopProvider{},
			},
			wantErr: true,
		},
		{
			// C: durable + noop writer → CheckNotNoop rejects
			name: "C_durable_noop_writer",
			cfg: cell.EmitterConfig{
				CellID:          "testcell",
				Mode:            cell.DurabilityDurable,
				Publisher:       realPub,
				OutboxWriter:    noopWriter,
				TxRunner:        realTx,
				MetricsProvider: metrics.NopProvider{},
			},
			wantErr: true,
		},
		{
			// D: demo + real pub, no writer → DirectEmitter(FailOpen), durable=false
			name: "D_demo_pub_no_writer",
			cfg: cell.EmitterConfig{
				CellID:            "testcell",
				Mode:              cell.DurabilityDemo,
				Publisher:         realPub,
				OutboxWriter:      nil,
				TxRunner:          nil,
				DirectPublishMode: outbox.DirectPublishFailOpen,
				MetricsProvider:   metrics.NopProvider{},
			},
			wantDurable: false,
		},
		{
			// E: demo + real pub + noop writer → DirectEmitter, durable=false
			name: "E_demo_pub_noop_writer",
			cfg: cell.EmitterConfig{
				CellID:            "testcell",
				Mode:              cell.DurabilityDemo,
				Publisher:         realPub,
				OutboxWriter:      noopWriter,
				TxRunner:          noopTx,
				DirectPublishMode: outbox.DirectPublishFailOpen,
				MetricsProvider:   metrics.NopProvider{},
			},
			wantDurable: false,
		},
		{
			// F: demo + real writer + real tx (no pub) → WriterEmitter, durable=true
			name: "F_demo_writer_with_tx",
			cfg: cell.EmitterConfig{
				CellID:          "testcell",
				Mode:            cell.DurabilityDemo,
				Publisher:       nil,
				OutboxWriter:    realW,
				TxRunner:        realTx,
				MetricsProvider: metrics.NopProvider{},
			},
			wantDurable: true,
		},
		{
			// G: demo + real writer but no tx → pairing invariant error
			name: "G_demo_writer_without_tx",
			cfg: cell.EmitterConfig{
				CellID:          "testcell",
				Mode:            cell.DurabilityDemo,
				Publisher:       nil,
				OutboxWriter:    realW,
				TxRunner:        nil,
				MetricsProvider: metrics.NopProvider{},
			},
			wantErr: true,
		},
		{
			// H: demo + all nil → no sink error
			name: "H_demo_all_nil",
			cfg: cell.EmitterConfig{
				CellID:          "testcell",
				Mode:            cell.DurabilityDemo,
				Publisher:       nil,
				OutboxWriter:    nil,
				TxRunner:        nil,
				MetricsProvider: metrics.NopProvider{},
			},
			wantErr: true,
		},
		{
			// I: demo + noop pub + noop writer + noop tx
			// noopPub is Nooper, noopWriter is Nooper → publisher branch selected → DirectEmitter, durable=false
			name: "I_demo_all_noop",
			cfg: cell.EmitterConfig{
				CellID:            "testcell",
				Mode:              cell.DurabilityDemo,
				Publisher:         noopPub,
				OutboxWriter:      noopWriter,
				TxRunner:          noopTx,
				DirectPublishMode: outbox.DirectPublishFailOpen,
				MetricsProvider:   metrics.NopProvider{},
			},
			wantDurable: false,
		},
		{
			// J: configcore fail-closed → DirectEmitter(FailClosed), durable=false
			name: "J_demo_fail_closed",
			cfg: cell.EmitterConfig{
				CellID:            "configcell",
				Mode:              cell.DurabilityDemo,
				Publisher:         realPub,
				OutboxWriter:      nil,
				TxRunner:          nil,
				DirectPublishMode: outbox.DirectPublishFailClosed,
				MetricsProvider:   metrics.NopProvider{},
			},
			wantDurable: false,
		},
		{
			// K: demo + real pub + real writer + real tx → WriterEmitter wins
			// (writer-present-and-non-noop branch is preferred over publisher).
			// Durable=true because outboxWriter is non-noop; publisher silently ignored.
			// Documenting this preference here prevents regressions of the dual-sink
			// contract.
			name: "K_demo_pub_and_real_writer",
			cfg: cell.EmitterConfig{
				CellID:            "testcell",
				Mode:              cell.DurabilityDemo,
				Publisher:         realPub,
				OutboxWriter:      realW,
				TxRunner:          realTx,
				DirectPublishMode: outbox.DirectPublishFailOpen,
				MetricsProvider:   metrics.NopProvider{},
			},
			wantDurable: true,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			outcome, err := cell.ResolveEmitter(tc.cfg)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil (outcome=%+v)", outcome)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if outcome.Emitter == nil {
				t.Fatal("expected non-nil Emitter")
			}
			if outcome.Durable != tc.wantDurable {
				t.Fatalf("Durable: got %v, want %v", outcome.Durable, tc.wantDurable)
			}
		})
	}
}

// nonDurableEmitter is an outbox.Emitter that deliberately does NOT implement
// DurabilityReporter, so outbox.ReportDurable returns false.
type nonDurableEmitter struct{}

func (nonDurableEmitter) Emit(_ context.Context, _ outbox.Entry) error { return nil }

// durableEmitter implements DurabilityReporter and reports Durable()==true.
type durableEmitter struct{}

func (durableEmitter) Emit(_ context.Context, _ outbox.Entry) error { return nil }
func (durableEmitter) Durable() bool                                { return true }

// TestResolveCellEmitter covers the Cell-boundary wrapper: mutual exclusion,
// WithEmitter durable-mode guard, delegation to ResolveEmitter, and the L2
// non-durable Warn log.
func TestResolveCellEmitter(t *testing.T) {
	t.Parallel()

	realPub := fakePublisher{}

	captureLogger := func() (*slog.Logger, *bytes.Buffer) {
		var buf bytes.Buffer
		h := slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})
		return slog.New(h), &buf
	}

	t.Run("mutual_exclusion", func(t *testing.T) {
		t.Parallel()
		_, err := cell.ResolveCellEmitter(cell.CellEmitterInputs{
			EmitterConfig: cell.EmitterConfig{
				CellID:    "testcell",
				Mode:      cell.DurabilityDemo,
				Publisher: realPub,
			},
			PreResolved: nonDurableEmitter{},
		})
		if err == nil {
			t.Fatal("expected mutual-exclusion error, got nil")
		}
		if !strings.Contains(err.Error(), "mutually exclusive") {
			t.Fatalf("error message missing 'mutually exclusive': %v", err)
		}
	})

	t.Run("preresolved_durable_mode_requires_durable", func(t *testing.T) {
		t.Parallel()
		_, err := cell.ResolveCellEmitter(cell.CellEmitterInputs{
			EmitterConfig: cell.EmitterConfig{
				CellID: "testcell",
				Mode:   cell.DurabilityDurable,
			},
			PreResolved: nonDurableEmitter{},
		})
		if err == nil {
			t.Fatal("expected durable-mode guard error, got nil")
		}
		if !strings.Contains(err.Error(), "durable") {
			t.Fatalf("error message missing 'durable': %v", err)
		}
	})

	t.Run("preresolved_durable_ok", func(t *testing.T) {
		t.Parallel()
		outcome, err := cell.ResolveCellEmitter(cell.CellEmitterInputs{
			EmitterConfig: cell.EmitterConfig{
				CellID: "testcell",
				Mode:   cell.DurabilityDurable,
			},
			PreResolved: durableEmitter{},
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !outcome.Durable {
			t.Fatal("expected outcome.Durable=true for durableEmitter")
		}
	})

	t.Run("preresolved_demo_non_durable_warn_at_L2", func(t *testing.T) {
		t.Parallel()
		logger, buf := captureLogger()
		outcome, err := cell.ResolveCellEmitter(cell.CellEmitterInputs{
			EmitterConfig: cell.EmitterConfig{
				CellID: "testcell",
				Mode:   cell.DurabilityDemo,
				Logger: logger,
			},
			PreResolved:      nonDurableEmitter{},
			ConsistencyLevel: cell.L2,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if outcome.Durable {
			t.Fatal("expected non-durable outcome")
		}
		if !strings.Contains(buf.String(), "L2 transactional atomicity not guaranteed") {
			t.Fatalf("expected L2 warn log, got: %q", buf.String())
		}
		if !strings.Contains(buf.String(), "durability_mode=demo") {
			t.Fatalf("expected durability_mode=demo in log, got: %q", buf.String())
		}
	})

	t.Run("preresolved_demo_no_warn_below_L2", func(t *testing.T) {
		t.Parallel()
		logger, buf := captureLogger()
		_, err := cell.ResolveCellEmitter(cell.CellEmitterInputs{
			EmitterConfig: cell.EmitterConfig{
				CellID: "testcell",
				Mode:   cell.DurabilityDemo,
				Logger: logger,
			},
			PreResolved:      nonDurableEmitter{},
			ConsistencyLevel: cell.L1,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if strings.Contains(buf.String(), "L2 transactional atomicity") {
			t.Fatalf("expected no L2 warn at L1, got: %q", buf.String())
		}
	})

	t.Run("delegates_to_resolve_emitter_on_demo", func(t *testing.T) {
		t.Parallel()
		logger, buf := captureLogger()
		outcome, err := cell.ResolveCellEmitter(cell.CellEmitterInputs{
			EmitterConfig: cell.EmitterConfig{
				CellID:            "testcell",
				Mode:              cell.DurabilityDemo,
				Publisher:         realPub,
				DirectPublishMode: outbox.DirectPublishFailClosed,
				Logger:            logger,
				MetricsProvider:   metrics.NopProvider{},
			},
			ConsistencyLevel: cell.L2,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if outcome.Emitter == nil {
			t.Fatal("expected non-nil Emitter")
		}
		if outcome.Durable {
			t.Fatal("expected non-durable DirectEmitter")
		}
		if !strings.Contains(buf.String(), "L2 transactional atomicity not guaranteed") {
			t.Fatalf("expected L2 warn for non-durable demo path, got: %q", buf.String())
		}
	})

	t.Run("error_from_resolve_emitter_propagates", func(t *testing.T) {
		t.Parallel()
		_, err := cell.ResolveCellEmitter(cell.CellEmitterInputs{
			EmitterConfig: cell.EmitterConfig{
				CellID: "testcell",
				Mode:   cell.DurabilityDemo,
			},
			ConsistencyLevel: cell.L2,
		})
		if err == nil {
			t.Fatal("expected no-sink error from ResolveEmitter")
		}
	})
}

// TestResolveEmitter_DemoMode_NilMetricsProvider_ReturnsError asserts that
// demo mode with a direct publisher and nil MetricsProvider returns
// ErrCellMissingOutbox (fail-fast; callers must pass metrics.NopProvider{}
// explicitly in tests instead of relying on a silent fallback).
func TestResolveEmitter_DemoMode_NilMetricsProvider_ReturnsError(t *testing.T) {
	t.Parallel()
	realPub := fakePublisher{}
	_, err := cell.ResolveEmitter(cell.EmitterConfig{
		CellID:            "testcell",
		Mode:              cell.DurabilityDemo,
		Publisher:         realPub,
		OutboxWriter:      nil,
		TxRunner:          nil,
		DirectPublishMode: outbox.DirectPublishFailOpen,
		MetricsProvider:   nil, // intentionally nil — must fail
	})
	if err == nil {
		t.Fatal("expected ErrCellMissingOutbox for nil MetricsProvider, got nil")
	}
	var ce *errcode.Error
	if !errors.As(err, &ce) {
		t.Fatalf("expected errcode.Error, got %T: %v", err, err)
	}
	if ce.Code != errcode.ErrCellMissingOutbox {
		t.Fatalf("expected ErrCellMissingOutbox, got code=%s msg=%s", ce.Code, ce.Message)
	}
	if !strings.Contains(err.Error(), "MetricsProvider") {
		t.Fatalf("error message should mention MetricsProvider, got: %v", err)
	}
}
