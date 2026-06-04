package grpc

// insecure_warn_test.go — TDD coverage for the AllowInsecure non-loopback
// startup warning (GAP-1 follow-up B2). Mirrors the HTTP OPS-07 warning
// (runtime/bootstrap/bootstrap_phase7.go): serving plaintext on a non-loopback
// address is a legitimate mesh-sidecar posture (see TLSConfig.AllowInsecure
// doc), so this is an observability Warn, not a fail-closed gate. The check
// lives adapter-side because only the adapter knows its own TLSConfig and the
// resolved listener address (bootstrap holds no adapters/grpc dependency).

import (
	"log/slog"
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/pkg/testutil/sloghelper"
)

// fakeNonTCPAddr is a net.Addr that is NOT *net.TCPAddr (e.g. a bufconn address),
// proving the warning is skipped when the bound socket is not a TCP listener.
type fakeNonTCPAddr struct{}

func (fakeNonTCPAddr) Network() string { return "bufconn" }
func (fakeNonTCPAddr) String() string  { return "bufnet" }

// installCaptureLogger swaps the process slog default for a JSON handler over a
// concurrency-safe buffer, restoring the original on cleanup (t.Cleanup, not a
// bare defer, so the restore survives a t.FailNow in the subtest body).
func installCaptureLogger(t *testing.T) *sloghelper.SyncBuffer {
	t.Helper()
	buf := sloghelper.NewSyncBuffer()
	original := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(buf, nil)))
	t.Cleanup(func() { slog.SetDefault(original) })
	return buf
}

func TestWarnIfInsecureNonLoopback(t *testing.T) {
	const warnMsgSubstr = "non-loopback"

	cases := []struct {
		name          string
		allowInsecure bool
		addr          net.Addr
		wantWarn      bool
		wantWildcard  bool
	}{
		{"insecure + non-loopback IPv4 warns", true, &net.TCPAddr{IP: net.IPv4(10, 0, 0, 1), Port: 9000}, true, false},
		{"insecure + IPv4 wildcard warns + wildcard_bind", true, &net.TCPAddr{IP: net.IPv4zero, Port: 9000}, true, true},
		{"insecure + IPv6 unspecified warns + wildcard_bind", true, &net.TCPAddr{IP: net.IPv6unspecified, Port: 9000}, true, true},
		{"insecure + loopback IPv4 no warn", true, &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 9000}, false, false},
		{"insecure + loopback IPv6 no warn", true, &net.TCPAddr{IP: net.IPv6loopback, Port: 9000}, false, false},
		// IPv4-mapped IPv6 loopback: net.IP.IsLoopback() resolves via To4(), so
		// ::ffff:127.0.0.1 is correctly treated as loopback (no warn). Locks the
		// behavior a reviewer flagged as a potential false positive.
		{"insecure + IPv4-mapped loopback no warn", true, &net.TCPAddr{IP: net.ParseIP("::ffff:127.0.0.1"), Port: 9000}, false, false},
		{"insecure + non-TCP addr no warn", true, fakeNonTCPAddr{}, false, false},
		{"TLS + non-loopback no warn", false, &net.TCPAddr{IP: net.IPv4(10, 0, 0, 1), Port: 9000}, false, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			buf := installCaptureLogger(t)

			warnIfInsecureNonLoopback(tc.allowInsecure, tc.addr)

			entry := sloghelper.FindLogEntry(buf.String(), warnMsgSubstr)
			if !tc.wantWarn {
				assert.Nil(t, entry, "no warning expected for this configuration")
				return
			}
			require.NotNil(t, entry, "expected an insecure non-loopback warning")
			assert.Equal(t, "WARN", entry["level"])
			assert.Equal(t, tc.addr.String(), entry["addr"])
			assert.Equal(t, tc.wantWildcard, entry["wildcard_bind"])
		})
	}
}
