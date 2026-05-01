// F06: table-driven tests for internalGuardFromEnv behavior.
//
// SEC-FAIL-CLOSED: the previous "dev mode silent bypass" (return nil, nil with
// a slog.Warn when secret is empty in non-real modes) has been removed.
// GOCELL_SERVICE_SECRET is now required in ALL adapter modes.
// This file verifies the new uniform error behavior.
package main

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/runtime/auth"
)

// captureSlogWarnLines installs a JSON slog handler capturing Warn-and-above
// records into a buffer. Returns the buffer and a restore function.
// The restore must be called via t.Cleanup to avoid polluting other tests.
//
// NOT concurrency-safe: callers must not run parallel sub-tests while this
// capture is active, because slog.SetDefault replaces the global logger for
// the entire process.
func captureSlogWarnLines(t *testing.T) (*bytes.Buffer, func()) {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	return &buf, func() { slog.SetDefault(prev) }
}

// countWarnLines counts JSON log lines whose "level" == "WARN" in buf.
func countWarnLines(buf *bytes.Buffer) int {
	count := 0
	for line := range bytes.SplitSeq(buf.Bytes(), []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal(line, &rec); err != nil {
			continue
		}
		if rec["level"] == "WARN" {
			count++
		}
	}
	return count
}

func TestInternalGuardFromEnv_WarnLogging_TableDriven(t *testing.T) {
	tests := []struct {
		name        string
		adapterMode string
		secret      string // empty means t.Setenv("GOCELL_SERVICE_SECRET", "")
		wantWarnCnt int
		wantErr     bool
		wantGuard   bool
	}{
		{
			// SEC-FAIL-CLOSED: dev mode now also requires the secret.
			// Previously returned (nil, nil) with a slog.Warn; now returns an error.
			name:        "dev_no_secret",
			adapterMode: "",
			secret:      "",
			wantWarnCnt: 0,
			wantErr:     true,
			wantGuard:   false,
		},
		{
			name:        "dev_with_secret",
			adapterMode: "",
			// freshTestServiceSecret is called per-case below to keep each test hermetic.
			wantWarnCnt: 0,
			wantErr:     false,
			wantGuard:   true,
		},
		{
			name:        "real_no_secret",
			adapterMode: "real",
			secret:      "",
			wantWarnCnt: 0,
			wantErr:     true,
			wantGuard:   false,
		},
		{
			name:        "real_with_secret",
			adapterMode: "real",
			wantWarnCnt: 0,
			wantErr:     false,
			wantGuard:   true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Resolve secret: cases that need a real secret get a fresh one.
			secret := tc.secret
			if tc.wantGuard {
				secret = freshTestServiceSecret(t)
			}
			t.Setenv("GOCELL_SERVICE_SECRET", secret)

			buf, restore := captureSlogWarnLines(t)
			t.Cleanup(restore)

			guard, err := internalGuardFromEnv(tc.adapterMode, nil)

			if tc.wantErr {
				require.Error(t, err,
					"case %q: expected error but got nil", tc.name)
			} else {
				require.NoError(t, err,
					"case %q: unexpected error: %v", tc.name, err)
			}

			if tc.wantGuard {
				assert.NotNil(t, guard,
					"case %q: expected non-nil guard", tc.name)
			} else {
				assert.Nil(t, guard,
					"case %q: expected nil guard", tc.name)
			}

			assert.Equal(t, tc.wantWarnCnt, countWarnLines(buf),
				"case %q: unexpected number of slog.Warn lines", tc.name)

			// Deep assertions for specific cases.
			switch tc.name {
			case "dev_no_secret":
				// SEC-FAIL-CLOSED: dev mode now also returns ERR_CONTROLPLANE_SERVICE_SECRET_MISSING.
				var ec *errcode.Error
				require.ErrorAs(t, err, &ec,
					"dev_no_secret: error must be an *errcode.Error")
				assert.Equal(t, errcode.ErrControlplaneServiceSecretMissing, ec.Code,
					"dev_no_secret: error code must be ERR_CONTROLPLANE_SERVICE_SECRET_MISSING")
			case "real_no_secret":
				// internalGuardFromEnv must return ERR_CONTROLPLANE_SERVICE_SECRET_MISSING
				// when GOCELL_SERVICE_SECRET is empty in adapter mode "real".
				var ec *errcode.Error
				require.ErrorAs(t, err, &ec,
					"real_no_secret: error must be an *errcode.Error")
				assert.Equal(t, errcode.ErrControlplaneServiceSecretMissing, ec.Code,
					"real_no_secret: error code must be ERR_CONTROLPLANE_SERVICE_SECRET_MISSING")
			case "real_with_secret":
				// The guard must have a non-noop NonceStore installed.
				require.NotNil(t, guard, "real_with_secret: guard must not be nil")
				ns := guard.NonceStore()
				require.NotNil(t, ns, "real_with_secret: NonceStore must not be nil")
				assert.NotEqual(t, auth.NonceStoreKindNoop, ns.Kind(),
					"real_with_secret: NonceStore must not be noop in adapter mode real")
			}
		})
	}
}
