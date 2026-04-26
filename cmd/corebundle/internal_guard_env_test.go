// F06: table-driven tests for internalGuardFromEnv slog.Warn emission.
//
// Verifies that the "controlplane guard disabled" warning is emitted exactly
// once in dev mode with an empty secret, and NOT emitted in other branches
// (dev+secret, real+no-secret-returns-error, real+secret-installs-guard).
package main

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// captureSlogWarnLines installs a JSON slog handler capturing Warn-and-above
// records into a buffer. Returns the buffer and a restore function.
// The restore must be called via t.Cleanup to avoid polluting other tests.
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
	for _, line := range bytes.Split(buf.Bytes(), []byte("\n")) {
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
			name:        "dev_no_secret",
			adapterMode: "",
			secret:      "",
			wantWarnCnt: 1,
			wantErr:     false,
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
		})
	}
}
