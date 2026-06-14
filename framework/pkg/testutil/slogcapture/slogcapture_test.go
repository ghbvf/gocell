package slogcapture_test

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/framework/pkg/testutil/slogcapture"
)

// TestInstallDefault_RedirectsAndRestores proves the sanctioned primitive both
// redirects slog.Default() to the supplied logger AND restores the previous
// default on cleanup. Restoration is observed across a sub-test boundary: the
// sub-test's t.Cleanup runs when t.Run returns, so the parent can assert the
// default is back.
func TestInstallDefault_RedirectsAndRestores(t *testing.T) {
	before := slog.Default()

	var buf bytes.Buffer
	t.Run("redirected", func(t *testing.T) {
		slogcapture.InstallDefault(t, slog.New(slog.NewJSONHandler(&buf, nil)))
		slog.Info("captured-line")
	})

	// After the sub-test returns, its t.Cleanup must have restored the default.
	if slog.Default() != before {
		t.Fatalf("slog.Default() not restored after sub-test cleanup")
	}
	if got := buf.String(); !strings.Contains(got, "captured-line") {
		t.Fatalf("emission was not routed to the installed handler; buffer=%q", got)
	}
}

// TestInstallDefault_LastWriterWins confirms nested installs stack and unwind in
// LIFO order via t.Cleanup, leaving the original default intact.
func TestInstallDefault_LastWriterWins(t *testing.T) {
	before := slog.Default()
	var outer, inner bytes.Buffer

	t.Run("outer", func(t *testing.T) {
		slogcapture.InstallDefault(t, slog.New(slog.NewJSONHandler(&outer, nil)))
		t.Run("inner", func(t *testing.T) {
			slogcapture.InstallDefault(t, slog.New(slog.NewJSONHandler(&inner, nil)))
			slog.Info("inner-line")
		})
		// inner cleanup ran; default is the outer handler again.
		slog.Info("outer-line")
	})

	if slog.Default() != before {
		t.Fatalf("slog.Default() not restored to original after all cleanups")
	}
	if !strings.Contains(inner.String(), "inner-line") || strings.Contains(inner.String(), "outer-line") {
		t.Fatalf("inner handler captured wrong lines; inner=%q", inner.String())
	}
	if !strings.Contains(outer.String(), "outer-line") {
		t.Fatalf("outer handler did not recapture after inner unwind; outer=%q", outer.String())
	}
}
