package cell_test

import (
	"context"
	"testing"

	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/persistence"
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
				CellID:       "testcell",
				Mode:         cell.DurabilityDurable,
				Publisher:    realPub,
				OutboxWriter: realW,
				TxRunner:     realTx,
			},
			wantDurable: true,
		},
		{
			// B: durable + missing writer → error (ErrCellMissingOutbox)
			name: "B_durable_missing_writer",
			cfg: cell.EmitterConfig{
				CellID:       "testcell",
				Mode:         cell.DurabilityDurable,
				Publisher:    realPub,
				OutboxWriter: nil,
				TxRunner:     realTx,
			},
			wantErr: true,
		},
		{
			// C: durable + noop writer → CheckNotNoop rejects
			name: "C_durable_noop_writer",
			cfg: cell.EmitterConfig{
				CellID:       "testcell",
				Mode:         cell.DurabilityDurable,
				Publisher:    realPub,
				OutboxWriter: noopWriter,
				TxRunner:     realTx,
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
			},
			wantDurable: false,
		},
		{
			// F: demo + real writer + real tx (no pub) → WriterEmitter, durable=true
			name: "F_demo_writer_with_tx",
			cfg: cell.EmitterConfig{
				CellID:       "testcell",
				Mode:         cell.DurabilityDemo,
				Publisher:    nil,
				OutboxWriter: realW,
				TxRunner:     realTx,
			},
			wantDurable: true,
		},
		{
			// G: demo + real writer but no tx → pairing invariant error
			name: "G_demo_writer_without_tx",
			cfg: cell.EmitterConfig{
				CellID:       "testcell",
				Mode:         cell.DurabilityDemo,
				Publisher:    nil,
				OutboxWriter: realW,
				TxRunner:     nil,
			},
			wantErr: true,
		},
		{
			// H: demo + all nil → no sink error
			name: "H_demo_all_nil",
			cfg: cell.EmitterConfig{
				CellID:       "testcell",
				Mode:         cell.DurabilityDemo,
				Publisher:    nil,
				OutboxWriter: nil,
				TxRunner:     nil,
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

// TestDirectPublishModeForDurability covers the 4-case policy selection table
// for the DirectPublishMode helper. The helper centralizes the demo-vs-durable
// branching that accesscore/auditcore/configcore previously encoded inline.
//
// ref: PR-A5c F2 / A5a-R4 DIRECTPUBLISHMODE-HELPER-DOWNSTREAM-01.
func TestDirectPublishModeForDurability(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		mode          cell.DurabilityMode
		demoPolicy    outbox.DirectPublishFailureMode
		durablePolicy outbox.DirectPublishFailureMode
		want          outbox.DirectPublishFailureMode
	}{
		{
			name:          "demo_picks_demo_policy_failopen",
			mode:          cell.DurabilityDemo,
			demoPolicy:    outbox.DirectPublishFailOpen,
			durablePolicy: outbox.DirectPublishFailClosed,
			want:          outbox.DirectPublishFailOpen,
		},
		{
			name:          "demo_picks_demo_policy_failclosed",
			mode:          cell.DurabilityDemo,
			demoPolicy:    outbox.DirectPublishFailClosed,
			durablePolicy: outbox.DirectPublishFailOpen,
			want:          outbox.DirectPublishFailClosed,
		},
		{
			name:          "durable_picks_durable_policy_failclosed",
			mode:          cell.DurabilityDurable,
			demoPolicy:    outbox.DirectPublishFailOpen,
			durablePolicy: outbox.DirectPublishFailClosed,
			want:          outbox.DirectPublishFailClosed,
		},
		{
			name:          "durable_picks_durable_policy_failopen",
			mode:          cell.DurabilityDurable,
			demoPolicy:    outbox.DirectPublishFailClosed,
			durablePolicy: outbox.DirectPublishFailOpen,
			want:          outbox.DirectPublishFailOpen,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := cell.DirectPublishModeForDurability(tc.mode, tc.demoPolicy, tc.durablePolicy)
			if got != tc.want {
				t.Fatalf("DirectPublishModeForDurability(%v,%v,%v): got %v, want %v",
					tc.mode, tc.demoPolicy, tc.durablePolicy, got, tc.want)
			}
		})
	}
}
