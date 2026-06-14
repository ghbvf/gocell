//go:build archtest_fixture

// Package positive_green is a GREEN fixture for SLOG-CAPTURE-GLOBAL-FUNNEL-01:
// it redirects slog.Default() through the sanctioned holder
// slogcapture.InstallDefault — NOT a raw slog.SetDefault. 0 violations expected.
package positive_green

import (
	"io"
	"log/slog"
	"testing"

	"github.com/ghbvf/gocell/framework/pkg/testutil/slogcapture"
)

// UseSanctionedRedirect redirects the default logger via the single sanctioned
// holder — the funnel-compliant path (no raw slog.SetDefault).
func UseSanctionedRedirect(t *testing.T) {
	slogcapture.InstallDefault(t, slog.New(slog.NewJSONHandler(io.Discard, nil)))
}
