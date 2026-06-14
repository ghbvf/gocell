// F06: table-driven tests for buildInternalHMACRing behavior.
//
// SEC-FAIL-CLOSED: GOCELL_SERVICE_SECRET is required in ALL adapter modes.
// Missing secret returns an error regardless of mode (no dev-mode silent bypass).
package main

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/pkg/testutil/slogcapture"
)

func TestBuildInternalHMACRing_WarnLogging_TableDriven(t *testing.T) {
	tests := []struct {
		name        string
		adapterMode string
		secret      string // empty means t.Setenv("GOCELL_SERVICE_SECRET", "")
		wantWarnCnt int
		wantErr     bool
		wantRing    bool
	}{
		{
			// SEC-FAIL-CLOSED: dev mode now also requires the secret.
			// Previously returned (nil, nil) with a slog.Warn; now returns an error.
			name:        "dev_no_secret",
			adapterMode: "",
			secret:      "",
			wantWarnCnt: 0,
			wantErr:     true,
			wantRing:    false,
		},
		{
			name:        "dev_with_secret",
			adapterMode: "",
			// freshTestServiceSecret is called per-case below to keep each test hermetic.
			wantWarnCnt: 0,
			wantErr:     false,
			wantRing:    true,
		},
		{
			name:        "real_no_secret",
			adapterMode: "real",
			secret:      "",
			wantWarnCnt: 0,
			wantErr:     true,
			wantRing:    false,
		},
		{
			name:        "real_with_secret",
			adapterMode: "real",
			wantWarnCnt: 0,
			wantErr:     false,
			wantRing:    true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Resolve secret: cases that need a real secret get a fresh one.
			secret := tc.secret
			if tc.wantRing {
				secret = freshTestServiceSecret(t)
			}
			t.Setenv("GOCELL_SERVICE_SECRET", secret)

			buf := captureSlogWarnLines(t)

			ring, err := buildInternalHMACRing(tc.adapterMode)

			if tc.wantErr {
				require.Error(t, err,
					"case %q: expected error but got nil", tc.name)
			} else {
				require.NoError(t, err,
					"case %q: unexpected error: %v", tc.name, err)
			}

			if tc.wantRing {
				assert.NotNil(t, ring,
					"case %q: expected non-nil ring", tc.name)
			} else {
				assert.Nil(t, ring,
					"case %q: expected nil ring", tc.name)
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
				// buildInternalHMACRing must return ERR_CONTROLPLANE_SERVICE_SECRET_MISSING
				// when GOCELL_SERVICE_SECRET is empty in adapter mode "real".
				var ec *errcode.Error
				require.ErrorAs(t, err, &ec,
					"real_no_secret: error must be an *errcode.Error")
				assert.Equal(t, errcode.ErrControlplaneServiceSecretMissing, ec.Code,
					"real_no_secret: error code must be ERR_CONTROLPLANE_SERVICE_SECRET_MISSING")
			}
		})
	}
}

// captureSlogWarnLines installs a JSON slog handler capturing Warn-and-above
// records into a buffer. Returns the buffer; cleanup is registered via t.Cleanup.
//
// NOT concurrency-safe: callers must not run parallel sub-tests while this
// capture is active, because slog.SetDefault replaces the global logger for
// the entire process.
func captureSlogWarnLines(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	slogcapture.InstallDefault(t, slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	return &buf
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
