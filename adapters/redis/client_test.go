package redis

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	goredis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/testutil/testtime"
)

func TestConfigDefaults(t *testing.T) {
	cfg := Config{}
	cfg.defaults()

	assert.Equal(t, ModeStandalone, cfg.Mode)
	assert.Equal(t, "", cfg.Addr) // No unsafe localhost fallback.
	assert.Equal(t, testtime.EventuallyLong, cfg.DialTimeout)
	assert.Equal(t, testtime.D3s, cfg.ReadTimeout)
	assert.Equal(t, testtime.D3s, cfg.WriteTimeout)
	assert.Equal(t, testtime.CtxLong, cfg.DistLockTTL)
}

func TestConfigDefaultsPreserveExisting(t *testing.T) {
	cfg := Config{
		Addr:        "redis:6380",
		Mode:        ModeSentinel,
		DialTimeout: testtime.SelectAsyncSettle,
		ReadTimeout: testtime.D7s,
		DistLockTTL: testtime.D60s,
	}
	cfg.defaults()

	assert.Equal(t, ModeSentinel, cfg.Mode)
	assert.Equal(t, "redis:6380", cfg.Addr)
	assert.Equal(t, testtime.SelectAsyncSettle, cfg.DialTimeout)
	assert.Equal(t, testtime.D7s, cfg.ReadTimeout)
	assert.Equal(t, testtime.D7s, cfg.WriteTimeout) // Defaults to ReadTimeout.
	assert.Equal(t, testtime.D60s, cfg.DistLockTTL)
}

func TestClientHealth_Success(t *testing.T) {
	mock := newMockCmdable()
	client := newClientFromCmdable(mock, Config{})

	err := client.Health(context.Background())
	assert.NoError(t, err)
}

func TestClientHealth_Failure(t *testing.T) {
	mock := newMockCmdable()
	mock.pingErr = errMock
	client := newClientFromCmdable(mock, Config{})

	err := client.Health(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ERR_ADAPTER_REDIS_CONNECT")
	assert.Contains(t, err.Error(), "health check failed")
}

func TestClientClose_Success(t *testing.T) {
	mock := newMockCmdable()
	client := newClientFromCmdable(mock, Config{})

	err := client.Close(context.Background())
	assert.NoError(t, err)
	assert.True(t, mock.closed)
}

func TestClientClose_Failure(t *testing.T) {
	mock := newMockCmdable()
	mock.closeErr = errMock
	client := newClientFromCmdable(mock, Config{})

	err := client.Close(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ERR_ADAPTER_REDIS_CONNECT")
}

func TestClientConfigReturned(t *testing.T) {
	mock := newMockCmdable()
	cfg := Config{
		Addr: "custom:6379",
		DB:   3,
	}
	client := newClientFromCmdable(mock, cfg)

	got := client.Config()
	assert.Equal(t, "custom:6379", got.Addr)
	assert.Equal(t, 3, got.DB)
}

func TestClientConfigPreservesPassword(t *testing.T) {
	mock := newMockCmdable()
	cfg := Config{
		Addr:     "redis:6379",
		Password: "s3cret",
	}
	client := newClientFromCmdable(mock, cfg)

	got := client.Config()
	assert.Equal(t, "s3cret", got.Password, "Config() must preserve password for round-trip")
}

func TestConfigLogValueRedactsPassword(t *testing.T) {
	cfg := Config{
		Addr:     "redis:6379",
		Password: "s3cret",
		Mode:     ModeStandalone,
		DB:       2,
	}
	lv := cfg.LogValue()
	// LogValue should contain addr and db but NOT password.
	resolved := lv.Resolve().String()
	assert.Contains(t, resolved, "redis:6379")
	assert.Contains(t, resolved, "2")
	assert.NotContains(t, resolved, "s3cret", "LogValue must not contain password")
}

func TestClientPoolStats_NilProvider(t *testing.T) {
	mock := newMockCmdable()
	client := newClientFromCmdable(mock, Config{})

	stats := client.PoolStats()
	assert.Equal(t, PoolStats{}, stats, "mock client must return zero PoolStats")
}

func TestClientPoolStats_WithProvider(t *testing.T) {
	mock := newMockCmdable()
	client := newClientFromCmdable(mock, Config{})
	client.statsProvider = &mockPoolStatsProvider{
		stats: &goredis.PoolStats{
			Hits:       100,
			Misses:     5,
			Timeouts:   1,
			TotalConns: 10,
			IdleConns:  7,
			StaleConns: 2,
		},
	}

	stats := client.PoolStats()
	assert.Equal(t, uint32(100), stats.Hits)
	assert.Equal(t, uint32(5), stats.Misses)
	assert.Equal(t, uint32(1), stats.Timeouts)
	assert.Equal(t, uint32(10), stats.TotalConns)
	assert.Equal(t, uint32(7), stats.IdleConns)
	assert.Equal(t, uint32(2), stats.StaleConns)
}

func TestPoolStats_JSON_CamelCase(t *testing.T) {
	stats := PoolStats{Hits: 10, Misses: 2, TotalConns: 5, IdleConns: 3}
	b, err := json.Marshal(stats)
	require.NoError(t, err)
	s := string(b)
	assert.Contains(t, s, `"hits"`)
	assert.Contains(t, s, `"misses"`)
	assert.Contains(t, s, `"totalConns"`)
	assert.Contains(t, s, `"idleConns"`)
}

func TestNewClient_StandaloneEmptyAddr(t *testing.T) {
	_, err := NewClient(context.Background(), Config{Mode: ModeStandalone})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Config.Addr is required")
}

func TestNewClient_SentinelEmptyAddrs(t *testing.T) {
	_, err := NewClient(context.Background(), Config{
		Mode:           ModeSentinel,
		SentinelMaster: "mymaster",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "SentinelAddrs is required")
}

func TestNewClient_SentinelEmptyMaster(t *testing.T) {
	_, err := NewClient(context.Background(), Config{
		Mode:          ModeSentinel,
		SentinelAddrs: []string{"sentinel:26379"},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "SentinelMaster is required")
}

// ---------------------------------------------------------------------------
// Cluster mode (B10 PR-V1-REDIS-CLUSTER) — fail-fast guards
// ---------------------------------------------------------------------------

func TestNewClient_ClusterEmptyAddrs(t *testing.T) {
	_, err := NewClient(context.Background(), Config{Mode: ModeCluster})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ClusterAddrs is required")
}

// Cluster mode rejects Addr being set; the two address fields are mutually
// exclusive so users do not silently ship a misconfigured deployment that
// looks like cluster but routes through standalone code paths.
func TestNewClient_ClusterAddrSetWithCluster(t *testing.T) {
	_, err := NewClient(context.Background(), Config{
		Mode:         ModeCluster,
		Addr:         "127.0.0.1:6379",
		ClusterAddrs: []string{"127.0.0.1:7000"},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Addr must be empty")
}

// Redis cluster has no SELECT command (no logical DB selection), so DB must
// be zero. Fail fast at construction instead of letting EVAL fail at runtime.
func TestNewClient_ClusterNonZeroDBRejected(t *testing.T) {
	_, err := NewClient(context.Background(), Config{
		Mode:         ModeCluster,
		ClusterAddrs: []string{"127.0.0.1:7000", "127.0.0.1:7001"},
		DB:           1,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "DB is not supported in cluster mode")
}

func TestNewClient_ClusterRemoteNonTLS(t *testing.T) {
	_, err := NewClient(context.Background(), Config{
		Mode: ModeCluster,
		ClusterAddrs: []string{
			"prod-a.redis.example.internal:7000",
			"prod-b.redis.example.internal:7000",
		},
		DialTimeout: testtime.SlowPoll,
	})
	require.Error(t, err)
	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, errcode.ErrAdapterEndpointNotTLS, ec.Code,
		"cluster remote non-TLS endpoints must be rejected with ErrAdapterEndpointNotTLS")
}

func TestConfigLogValue_Cluster(t *testing.T) {
	cfg := Config{
		Mode:         ModeCluster,
		ClusterAddrs: []string{"node-a:7000", "node-b:7000"},
		Password:     "s3cret",
	}
	resolved := cfg.LogValue().Resolve().String()
	assert.Contains(t, resolved, "cluster", "LogValue should reflect mode=cluster")
	assert.Contains(t, resolved, "node-a:7000", "LogValue must show cluster addrs for ops diagnosis")
	assert.Contains(t, resolved, "node-b:7000")
	assert.NotContains(t, resolved, "s3cret", "LogValue must not leak password in cluster mode")
}

// rediss URL form may embed user:password in ClusterAddrs. LogValue must
// route every URL entry through url.URL.Redacted so the password segment
// is replaced with "xxxxx" before reaching slog; plain host:port entries
// pass through unchanged. Parse failures are masked with "<unparseable>"
// so a malformed URL never bypasses redaction.
func TestConfigLogValue_Cluster_RedactsURLCredentials(t *testing.T) {
	cfg := Config{
		Mode: ModeCluster,
		ClusterAddrs: []string{
			"rediss://acl-user:tops3cret@node-a.example.internal:7000",
			"node-plain:7000",
			"rediss://only-user@node-b.example.internal:7000",
		},
		Password: "envS3cret",
	}
	resolved := cfg.LogValue().Resolve().String()
	assert.NotContains(t, resolved, "tops3cret",
		"LogValue must redact URL-embedded password")
	assert.NotContains(t, resolved, "envS3cret",
		"LogValue must not leak the env-side Password field")
	assert.Contains(t, resolved, "xxxxx",
		"url.URL.Redacted replaces password with literal xxxxx")
	assert.Contains(t, resolved, "node-a.example.internal:7000",
		"hostname must remain visible for ops diagnosis")
	assert.Contains(t, resolved, "node-plain:7000",
		"plain host:port entries pass through verbatim")
	assert.Contains(t, resolved, "only-user",
		"username-only URLs keep the username visible (no password to mask)")
}

// go-redis ClusterClient default PoolSize is 5*GOMAXPROCS while standalone
// Client default is 10*GOMAXPROCS. Mirror that here so db.client.connection.max
// stays accurate for cluster pools and PoolSize is not silently inflated by
// 2x per node × N nodes.
func TestConfigDefaults_ClusterPoolSize(t *testing.T) {
	cfg := Config{
		Mode:         ModeCluster,
		ClusterAddrs: []string{"127.0.0.1:7000"},
	}
	cfg.defaults()
	if cfg.PoolSize <= 0 {
		t.Fatalf("PoolSize must be positive after defaults(), got %d", cfg.PoolSize)
	}
	standalone := Config{Addr: "127.0.0.1:6379"}
	standalone.defaults()
	if cfg.PoolSize >= standalone.PoolSize {
		t.Fatalf("cluster default PoolSize (%d) must be < standalone default (%d)",
			cfg.PoolSize, standalone.PoolSize)
	}
}

// TestBuildClusterOptions covers buildClusterOptions's URL/plain handling, TLS
// extraction, mixed-form rejection, and conflicting-field rejection. Mirrors
// buildFailoverOptions tests by intent — sentinel and cluster share the same
// URL parsing semantics.
func TestBuildClusterOptions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		cfg         Config
		expectError string // substring; "" means expect no error
	}{
		{
			name: "plain host:port preserves TLSConfig nil",
			cfg: Config{
				Mode:         ModeCluster,
				ClusterAddrs: []string{"127.0.0.1:7000", "127.0.0.1:7001"},
			},
		},
		{
			name: "rediss URL form sets TLSConfig with empty ServerName",
			cfg: Config{
				Mode: ModeCluster,
				ClusterAddrs: []string{
					"rediss://node-a.cluster.example.internal:7000",
					"rediss://node-b.cluster.example.internal:7000",
				},
			},
		},
		{
			name: "mixed URL and plain forms rejected",
			cfg: Config{
				Mode: ModeCluster,
				ClusterAddrs: []string{
					"rediss://node-a.cluster.example.internal:7000",
					"127.0.0.1:7001",
				},
			},
			expectError: "cannot mix URL and host:port",
		},
		{
			name: "conflicting password values rejected",
			cfg: Config{
				Mode: ModeCluster,
				ClusterAddrs: []string{
					"rediss://user:pass-a@node-a.cluster.example.internal:7000",
					"rediss://user:pass-b@node-b.cluster.example.internal:7000",
				},
			},
			expectError: "conflicting Cluster URL field values",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := tc.cfg
			cfg.defaults()
			opts, err := buildClusterOptions(cfg)
			if tc.expectError != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.expectError)
				return
			}
			require.NoError(t, err)
			require.NotNil(t, opts)
			require.Len(t, opts.Addrs, len(cfg.ClusterAddrs),
				"buildClusterOptions must populate one Addrs entry per ClusterAddrs entry")
			assertClusterTLSConfigShape(t, opts, cfg.ClusterAddrs)
		})
	}
}

// assertClusterTLSConfigShape pulls the TLSConfig assertions out of the
// table-driven loop body to keep the loop's cognitive complexity below the
// project ceiling. opts.TLSConfig must be set with empty ServerName when
// any rediss:// URL is present (crypto/tls infers SNI per node), and nil
// otherwise.
func assertClusterTLSConfigShape(t *testing.T, opts *goredis.ClusterOptions, addrs []string) {
	t.Helper()
	if !anyHasPrefix(addrs, "rediss://") {
		require.Nil(t, opts.TLSConfig)
		return
	}
	require.NotNil(t, opts.TLSConfig)
	require.Empty(t, opts.TLSConfig.ServerName,
		"shared cluster TLSConfig must let crypto/tls infer SNI per node")
}

func anyHasPrefix(addrs []string, prefix string) bool {
	for _, a := range addrs {
		if strings.HasPrefix(a, prefix) {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// T18: Client.Close(ctx) tests
// ---------------------------------------------------------------------------

func TestClientClose_AcceptsCtx(t *testing.T) {
	mock := newMockCmdable()
	client := newClientFromCmdable(mock, Config{})

	ctx, cancel := context.WithTimeout(context.Background(), testtime.CtxDefault)
	defer cancel()

	err := client.Close(ctx)
	assert.NoError(t, err)
	assert.True(t, mock.closed)
}

func TestClientClose_PreCancelledCtxReturnsError(t *testing.T) {
	mock := newMockCmdable()
	client := newClientFromCmdable(mock, Config{})

	cancelledCtx, cancel := context.WithCancel(context.Background())
	cancel() // already canceled

	err := client.Close(cancelledCtx)
	require.Error(t, err, "Close with pre-canceled ctx must return error")
}

func TestClientClose_ContextFailure(t *testing.T) {
	mock := newMockCmdable()
	mock.closeErr = errMock
	client := newClientFromCmdable(mock, Config{})

	err := client.Close(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ERR_ADAPTER_REDIS_CONNECT")
}

// TestClient_ImplementsContextCloser verifies the compile-time assertion at
// package level. This is a belt-and-suspenders test confirming the interface.
func TestClient_ImplementsContextCloser(t *testing.T) {
	var _ interface {
		Close(ctx context.Context) error
	} = (*Client)(nil)
}

// TestConfigValidate_RejectNonTLSRemote verifies that NewClient returns a TLS
// validation error (errcode.ErrAdapterEndpointNotTLS) for remote non-TLS
// endpoints, and accepts loopback addresses and TLS-scheme URLs without error.
//
// Loopback addresses (127.0.0.1) are exempt — they may fail with a connection
// refused error but must NOT produce a TLS validation error.
func TestConfigValidate_RejectNonTLSRemote(t *testing.T) {
	tests := []struct {
		name       string
		cfg        Config
		wantTLSErr bool // expect errcode.ErrAdapterEndpointNotTLS
	}{
		{
			name: "standalone remote non-TLS addr — reject",
			cfg: Config{
				Mode:        ModeStandalone,
				Addr:        "prod.redis.example.internal:6379",
				DialTimeout: testtime.SlowPoll,
			},
			wantTLSErr: true,
		},
		{
			name: "sentinel remote non-TLS addr — reject",
			cfg: Config{
				Mode:           ModeSentinel,
				SentinelAddrs:  []string{"prod1.sentinel.example.internal:26379"},
				SentinelMaster: "mymaster",
				DialTimeout:    testtime.SlowPoll,
			},
			wantTLSErr: true,
		},
		{
			name: "standalone loopback — ok (no TLS error expected)",
			cfg: Config{
				Mode:        ModeStandalone,
				Addr:        "127.0.0.1:6379",
				DialTimeout: testtime.SlowPoll,
			},
			wantTLSErr: false,
		},
		{
			name: "standalone rediss scheme — ok (TLS scheme accepted)",
			cfg: Config{
				Mode:        ModeStandalone,
				Addr:        "rediss://prod.redis.example.internal:6379",
				DialTimeout: testtime.SlowPoll,
			},
			wantTLSErr: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := NewClient(context.Background(), tc.cfg)
			assertNewClientTLSResult(t, tc.cfg, err, tc.wantTLSErr)
		})
	}
}

// assertNewClientTLSResult checks NewClient's error against the expected TLS
// validation outcome. When wantTLSErr is true the error must be a
// *errcode.Error tagged ErrAdapterEndpointNotTLS; for accepted endpoints the
// error (if any) may be a connection failure but must not be a TLS validation
// error. Extracted to keep TestConfigValidate_RejectNonTLSRemote's loop body
// within the cognitive-complexity budget.
func assertNewClientTLSResult(t *testing.T, cfg Config, err error, wantTLSErr bool) {
	t.Helper()
	if !wantTLSErr {
		if err == nil {
			return
		}
		var ec *errcode.Error
		if errors.As(err, &ec) && ec.Code == errcode.ErrAdapterEndpointNotTLS {
			t.Errorf("NewClient(%+v): unexpected TLS validation error: %v", cfg, err)
		}
		return
	}
	require.Error(t, err,
		"NewClient(%+v): expected TLS validation error, got nil", cfg)
	var ec *errcode.Error
	if !errors.As(err, &ec) || ec.Code != errcode.ErrAdapterEndpointNotTLS {
		t.Errorf("NewClient(%+v): error %q is not errcode.ErrAdapterEndpointNotTLS",
			cfg, err.Error())
	}
}

// TestBuildStandaloneOptions verifies that an addr accepted by
// ValidateTLSEndpoint is converted to go-redis Options with the right
// TLSConfig populated. SEC-FAIL-CLOSED requires the validator and the dial
// path to agree on TLS enforcement; without ParseURL wiring, rediss://
// would silently downgrade to plain TCP.
func TestBuildStandaloneOptions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		addr        string
		wantTLS     bool
		wantAddr    string
		wantDB      int
		expectError bool
	}{
		{
			name:     "rediss URL form sets TLSConfig",
			addr:     "rediss://prod.redis:6379",
			wantTLS:  true,
			wantAddr: "prod.redis:6379",
		},
		{
			name:     "redis URL form preserves TLSConfig nil",
			addr:     "redis://localhost:6379/3",
			wantTLS:  false,
			wantAddr: "localhost:6379",
			wantDB:   3,
		},
		{
			name:     "plain host port preserves TLSConfig nil",
			addr:     "127.0.0.1:6379",
			wantTLS:  false,
			wantAddr: "127.0.0.1:6379",
		},
		{
			name:        "malformed URL returns error",
			addr:        "rediss://[::z]:not-a-port",
			expectError: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := Config{Mode: ModeStandalone, Addr: tc.addr}
			cfg.defaults()
			opts, err := buildStandaloneOptions(cfg)
			if tc.expectError {
				require.Error(t, err, "expected parse error for %q", tc.addr)
				return
			}
			require.NoError(t, err, "addr=%q", tc.addr)
			require.Equal(t, tc.wantAddr, opts.Addr)
			if tc.wantTLS {
				require.NotNil(t, opts.TLSConfig,
					"TLSConfig must be set for %q so go-redis dials TLS", tc.addr)
			} else {
				require.Nil(t, opts.TLSConfig)
			}
			if tc.wantDB != 0 {
				require.Equal(t, tc.wantDB, opts.DB)
			}
		})
	}
}

func TestBuildFailoverOptions_RedissURLFormSetsTLSConfig(t *testing.T) {
	t.Parallel()

	cfg := Config{
		Mode:           ModeSentinel,
		SentinelAddrs:  []string{"rediss://sentinel-a.redis.example.internal:26379", "rediss://sentinel-b.redis.example.internal:26379"},
		SentinelMaster: "mymaster",
		Password:       "secret",
		DB:             2,
	}
	cfg.defaults()

	opts, err := buildFailoverOptions(cfg)
	require.NoError(t, err)
	require.Equal(t, "mymaster", opts.MasterName)
	require.Equal(t, []string{
		"sentinel-a.redis.example.internal:26379",
		"sentinel-b.redis.example.internal:26379",
	}, opts.SentinelAddrs)
	require.NotNil(t, opts.TLSConfig, "rediss Sentinel addresses must dial with TLS")
	require.Empty(t, opts.TLSConfig.ServerName,
		"shared failover TLSConfig must let crypto/tls infer SNI per Sentinel/master address")
	require.Equal(t, "secret", opts.Password)
	require.Equal(t, 2, opts.DB)
}

func TestBuildFailoverOptions_RedissURLPreservesSkipVerify(t *testing.T) {
	t.Parallel()

	cfg := Config{
		Mode:           ModeSentinel,
		SentinelAddrs:  []string{"rediss://sentinel.redis.example.internal:26379?skip_verify=true"},
		SentinelMaster: "mymaster",
	}
	cfg.defaults()

	opts, err := buildFailoverOptions(cfg)
	require.NoError(t, err)
	require.NotNil(t, opts.TLSConfig)
	require.True(t, opts.TLSConfig.InsecureSkipVerify)
	require.Empty(t, opts.TLSConfig.ServerName)
}

func TestBuildFailoverOptions_RedissURLCredentialsUseFailoverSemantics(t *testing.T) {
	t.Parallel()

	cfg := Config{
		Mode: ModeSentinel,
		SentinelAddrs: []string{
			"rediss://sentinel-user:sentinel-pass@sentinel.redis.example.internal:26379?username=data-user&password=data-pass",
		},
		SentinelMaster: "mymaster",
	}
	cfg.defaults()

	opts, err := buildFailoverOptions(cfg)
	require.NoError(t, err)
	require.Equal(t, "sentinel-user", opts.SentinelUsername)
	require.Equal(t, "sentinel-pass", opts.SentinelPassword)
	require.Equal(t, "data-user", opts.Username)
	require.Equal(t, "data-pass", opts.Password)
}

func TestBuildFailoverOptions_RejectsInconsistentSentinelURLTLSSettings(t *testing.T) {
	t.Parallel()

	cfg := Config{
		Mode: ModeSentinel,
		SentinelAddrs: []string{
			"rediss://sentinel-a.redis.example.internal:26379?skip_verify=true",
			"rediss://sentinel-b.redis.example.internal:26379",
		},
		SentinelMaster: "mymaster",
	}
	cfg.defaults()

	_, err := buildFailoverOptions(cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "same TLS verification settings")
}

func TestBuildFailoverOptions_RejectsMixedSentinelAddressForms(t *testing.T) {
	t.Parallel()

	cfg := Config{
		Mode:           ModeSentinel,
		SentinelAddrs:  []string{"rediss://sentinel-a.redis.example.internal:26379", "sentinel-b.redis.example.internal:26379"},
		SentinelMaster: "mymaster",
	}
	cfg.defaults()

	_, err := buildFailoverOptions(cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot mix URL and host:port")
}
