package grpclistener

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	adaptersgrpc "github.com/ghbvf/gocell/adapters/grpc"
	"github.com/ghbvf/gocell/framework/kernel/outbox"
)

// TestAddrFromEnv verifies that AddrFromEnv returns the default address when
// GOCELL_GRPC_ADDR is unset and the override value when it is set.
// Subtests use t.Setenv and therefore must NOT call t.Parallel.
func TestAddrFromEnv(t *testing.T) {
	tests := []struct {
		name    string
		envVal  string
		wantSet bool
		want    string
	}{
		{
			name:    "default when unset",
			envVal:  "",
			wantSet: false,
			want:    DefaultAddr,
		},
		{
			name:    "override when set",
			envVal:  ":9999",
			wantSet: true,
			want:    ":9999",
		},
		{
			name:    "trims whitespace",
			envVal:  "  :9090  ",
			wantSet: true,
			want:    ":9090",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.wantSet {
				t.Setenv(EnvAddr, tc.envVal)
			}
			got := AddrFromEnv(PlatformEnv)
			if got != tc.want {
				t.Errorf("AddrFromEnv() = %q; want %q", got, tc.want)
			}
		})
	}
}

func TestAddrFromEnv_CustomEnvConfig(t *testing.T) {
	env := EnvConfig{Prefix: "GOCELL_IOTDEVICE_GRPC", DefaultAddr: ":8084"}

	t.Run("custom default when unset", func(t *testing.T) {
		if got := AddrFromEnv(env); got != ":8084" {
			t.Errorf("AddrFromEnv(custom) = %q; want :8084", got)
		}
	})

	t.Run("custom prefix override", func(t *testing.T) {
		t.Setenv("GOCELL_IOTDEVICE_GRPC_ADDR", "  127.0.0.1:18084  ")
		if got := AddrFromEnv(env); got != "127.0.0.1:18084" {
			t.Errorf("AddrFromEnv(custom) = %q; want 127.0.0.1:18084", got)
		}
	})
}

// TestTLSConfigFromEnv verifies the six TLS posture cases for tlsConfigFromEnv.
// Subtests use t.Setenv and therefore must NOT call t.Parallel.
func TestTLSConfigFromEnv(t *testing.T) {
	// makeTempPEM writes a minimal placeholder PEM file to a temp directory and
	// returns its path. The content is not a valid certificate — we only test
	// that the file is read and its bytes end up in the returned TLSConfig.
	makeTempPEM := func(t *testing.T, name, content string) string {
		t.Helper()
		dir := t.TempDir()
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatalf("write temp PEM %s: %v", name, err)
		}
		return path
	}

	tests := []struct {
		name              string
		mode              outbox.DurabilityMode
		setEnv            func(t *testing.T)
		wantErr           bool
		wantAllowInsecure bool
		wantCertNonNil    bool
		wantKeyNonNil     bool
		wantCANonNil      bool
	}{
		{
			name: "(a) demo mode no TLS env — returns AllowInsecure no error",
			mode: outbox.DurabilityDemo,
			setEnv: func(t *testing.T) {
				// no TLS env vars set
			},
			wantErr:           false,
			wantAllowInsecure: true,
		},
		{
			name: "(b) durable mode no TLS env no ALLOW_INSECURE — error fail-closed",
			mode: outbox.DurabilityDurable,
			setEnv: func(t *testing.T) {
				// no TLS env vars set
			},
			wantErr: true,
		},
		{
			name: "(c) durable mode ALLOW_INSECURE=true — AllowInsecure no error",
			mode: outbox.DurabilityDurable,
			setEnv: func(t *testing.T) {
				t.Setenv(EnvAllowInsecure, "true")
			},
			wantErr:           false,
			wantAllowInsecure: true,
		},
		{
			name: "(d) cert+key env set — CertPEM/KeyPEM populated no error",
			mode: outbox.DurabilityDurable,
			setEnv: func(t *testing.T) {
				certPath := makeTempPEM(t, "cert.pem", "CERT_DATA")
				keyPath := makeTempPEM(t, "key.pem", "KEY_DATA")
				t.Setenv(EnvTLSCertFile, certPath)
				t.Setenv(EnvTLSKeyFile, keyPath)
			},
			wantErr:        false,
			wantCertNonNil: true,
			wantKeyNonNil:  true,
		},
		{
			name: "(e) cert set but key missing — error",
			mode: outbox.DurabilityDurable,
			setEnv: func(t *testing.T) {
				certPath := makeTempPEM(t, "cert.pem", "CERT_DATA")
				t.Setenv(EnvTLSCertFile, certPath)
				// EnvTLSKeyFile not set
			},
			wantErr: true,
		},
		{
			name: "(f) cert+key+clientCA — ClientCAPEM populated",
			mode: outbox.DurabilityDurable,
			setEnv: func(t *testing.T) {
				certPath := makeTempPEM(t, "cert.pem", "CERT_DATA")
				keyPath := makeTempPEM(t, "key.pem", "KEY_DATA")
				caPath := makeTempPEM(t, "ca.pem", "CA_DATA")
				t.Setenv(EnvTLSCertFile, certPath)
				t.Setenv(EnvTLSKeyFile, keyPath)
				t.Setenv(EnvTLSClientCAFile, caPath)
			},
			wantErr:        false,
			wantCertNonNil: true,
			wantKeyNonNil:  true,
			wantCANonNil:   true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tc.setEnv(t)

			got, err := tlsConfigFromEnv(PlatformEnv, tc.mode)

			if tc.wantErr {
				if err == nil {
					t.Errorf("expected error; got nil with TLSConfig=%+v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			assertTLSConfig(t, got, tc.wantAllowInsecure, tc.wantCertNonNil, tc.wantKeyNonNil, tc.wantCANonNil)
		})
	}
}

func TestTLSConfigFromEnv_CustomEnvConfig(t *testing.T) {
	env := EnvConfig{Prefix: "GOCELL_IOTDEVICE_GRPC", DefaultAddr: ":8084"}

	t.Run("durable mode honors custom allow insecure env", func(t *testing.T) {
		t.Setenv("GOCELL_IOTDEVICE_GRPC_ALLOW_INSECURE", "true")

		got, err := tlsConfigFromEnv(env, outbox.DurabilityDurable)
		if err != nil {
			t.Fatalf("tlsConfigFromEnv(custom) unexpected error: %v", err)
		}
		assertTLSConfig(t, got, true, false, false, false)
	})

	t.Run("custom tls env names are used in errors", func(t *testing.T) {
		t.Setenv("GOCELL_IOTDEVICE_GRPC_TLS_CERT_FILE", filepath.Join(t.TempDir(), "cert.pem"))

		_, err := tlsConfigFromEnv(env, outbox.DurabilityDurable)
		if err == nil {
			t.Fatal("expected error for missing key")
		}
		if got := err.Error(); !strings.Contains(got, "GOCELL_IOTDEVICE_GRPC_TLS_CERT_FILE") ||
			!strings.Contains(got, "GOCELL_IOTDEVICE_GRPC_TLS_KEY_FILE") {
			t.Fatalf("error = %q; want custom TLS env names", got)
		}
	})
}

func assertTLSConfig(
	t *testing.T,
	cfg adaptersgrpc.TLSConfig,
	wantAllowInsecure, wantCertNonNil, wantKeyNonNil, wantCANonNil bool,
) {
	t.Helper()

	if cfg.AllowInsecure != wantAllowInsecure {
		t.Errorf("AllowInsecure = %v; want %v", cfg.AllowInsecure, wantAllowInsecure)
	}
	if wantCertNonNil && len(cfg.CertPEM) == 0 {
		t.Errorf("CertPEM is empty; want non-empty")
	}
	if !wantCertNonNil && len(cfg.CertPEM) != 0 {
		t.Errorf("CertPEM is non-empty; want empty")
	}
	if wantKeyNonNil && len(cfg.KeyPEM) == 0 {
		t.Errorf("KeyPEM is empty; want non-empty")
	}
	if !wantKeyNonNil && len(cfg.KeyPEM) != 0 {
		t.Errorf("KeyPEM is non-empty; want empty")
	}
	if wantCANonNil && len(cfg.ClientCAPEM) == 0 {
		t.Errorf("ClientCAPEM is empty; want non-empty")
	}
	if !wantCANonNil && len(cfg.ClientCAPEM) != 0 {
		t.Errorf("ClientCAPEM is non-empty; want empty")
	}
}
