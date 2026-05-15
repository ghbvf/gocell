package oidc

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	gooidc "github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	"github.com/ghbvf/gocell/adapters/adapterutil"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/lifecycle"
	"github.com/ghbvf/gocell/kernel/worker"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/secutil"
)

// Compile-time assertion: Adapter satisfies lifecycle.ManagedResource.
var _ lifecycle.ManagedResource = (*Adapter)(nil)

const (
	// defaultOIDCHTTPTimeout is the default HTTP client timeout for OIDC
	// provider discovery and token exchange requests.
	defaultOIDCHTTPTimeout = 10 * time.Second

	// defaultOIDCRefreshInterval is the default period between periodic
	// full OIDC provider re-discovery runs. Configurable via Config.RefreshInterval.
	// go-oidc v3.18 handles JWKS key rotation reactively (kid-miss refetch);
	// this interval only refreshes discovery metadata (jwks_uri, endpoints, alg).
	defaultOIDCRefreshInterval = 24 * time.Hour
)

// Config holds the OIDC provider configuration.
type Config struct {
	IssuerURL    string
	ClientID     string
	ClientSecret string
	RedirectURL  string
	Scopes       []string      // default: [openid, profile, email]
	HTTPTimeout  time.Duration // default: 10s

	// Clock is the time source injected by the composition root or tests.
	// Required: New panics via clock.MustHaveClock when Clock is nil.
	// Production wiring: clock.Real(); tests: clockmock.New(t).
	Clock clock.Clock

	// RefreshInterval controls how often the worker re-discovers OIDC provider
	// metadata. Zero means defaultOIDCRefreshInterval (24h).
	RefreshInterval time.Duration

	// RefreshCollector receives success/failure signals from the refresh worker.
	// Optional: nil is replaced with NoopRefreshCollector{} in New().
	RefreshCollector RefreshCollector
}

// Validate checks required fields.
func (c Config) Validate() error {
	if c.IssuerURL == "" {
		return errcode.New(errcode.KindInternal, ErrAdapterOIDCConfig, "oidc: issuer URL is required")
	}
	// SEC-FAIL-CLOSED: reject non-TLS issuer endpoints (loopback exempt).
	if err := secutil.ValidateTLSEndpoint(c.IssuerURL); err != nil {
		return err
	}
	if c.ClientID == "" {
		return errcode.New(errcode.KindInternal, ErrAdapterOIDCConfig, "oidc: client ID is required")
	}
	return nil
}

// Adapter is a thin wrapper over go-oidc and oauth2. It manages provider
// discovery and exposes the underlying go-oidc types directly.
type Adapter struct {
	config           Config
	client           *http.Client
	clk              clock.Clock
	refreshCollector RefreshCollector

	mu       sync.RWMutex
	provider *gooidc.Provider

	// consecutiveFailures counts how many consecutive refresh attempts have
	// failed since the last success. Reset to 0 on any successful refresh.
	consecutiveFailures atomic.Int64

	// started is set to true at the very beginning of runRefreshLoop so that
	// Close and Stop can skip the workerDone drain when the worker goroutine
	// was never started (e.g. bootstrap aborted before Worker.Start).
	started atomic.Bool

	stopOnce   sync.Once
	stopCh     chan struct{} // signals the worker goroutine to exit
	workerDone chan struct{} // closed when the worker goroutine returns
}

// New creates an OIDC Adapter and synchronously performs OIDC discovery.
// An unreachable or misconfigured issuer causes construction to fail
// immediately (fail-fast at boot, not at first request).
//
// Clock is required: New panics via clock.MustHaveClock when cfg.Clock is nil.
// Use clock.Real() at the composition root; inject clockmock.New() in tests.
//
// ref: coreos/go-oidc — Provider.NewProvider semantics (sync HTTP round-trip).
// ref: adapters/s3.New — same clock.MustHaveClock + state-machine field pattern.
func New(ctx context.Context, cfg Config) (*Adapter, error) {
	clock.MustHaveClock(cfg.Clock, "oidc.New")

	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	timeout := cfg.HTTPTimeout
	if timeout == 0 {
		timeout = defaultOIDCHTTPTimeout
	}

	rc := cfg.RefreshCollector
	if rc == nil {
		rc = NoopRefreshCollector{}
	}

	a := &Adapter{
		config:           cfg,
		client:           &http.Client{Timeout: timeout},
		clk:              cfg.Clock,
		refreshCollector: rc,
		stopCh:           make(chan struct{}),
		workerDone:       make(chan struct{}),
	}
	if _, err := a.discover(ctx, true); err != nil {
		return nil, err
	}
	return a, nil
}

func (a *Adapter) oidcCtx(ctx context.Context) context.Context {
	return gooidc.ClientContext(ctx, a.client)
}

// Provider returns the go-oidc Provider, performing discovery on first call.
// Subsequent calls return the cached provider without re-fetching.
//
// IMPORTANT: The cached provider never expires automatically. For long-lived
// processes, the caller (typically bootstrap/runtime) MUST call Refresh()
// periodically to pick up OIDC metadata and JWKS key rotation. A common
// pattern is a background goroutine with a 24h ticker.
func (a *Adapter) Provider(ctx context.Context) (*gooidc.Provider, error) {
	a.mu.RLock()
	if a.provider != nil {
		p := a.provider
		a.mu.RUnlock()
		return p, nil
	}
	a.mu.RUnlock()

	return a.discover(ctx, false)
}

// Refresh forces re-discovery of the OIDC provider metadata. Use this
// periodically in long-lived processes to pick up JWKS/metadata rotation.
func (a *Adapter) Refresh(ctx context.Context) (*gooidc.Provider, error) {
	return a.discover(ctx, true)
}

// discover performs OIDC discovery. When force is false, it double-checks
// whether another goroutine already completed initialization before making
// a network call (cold-start thundering-herd protection).
func (a *Adapter) discover(ctx context.Context, force bool) (*gooidc.Provider, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if !force && a.provider != nil {
		return a.provider, nil
	}

	p, err := gooidc.NewProvider(a.oidcCtx(ctx), a.config.IssuerURL)
	if err != nil {
		return nil, errcode.Wrap(errcode.KindInternal, ErrAdapterOIDCDiscovery,
			"oidc: discovery failed", err,
			errcode.WithDetails(slog.String("issuer", a.config.IssuerURL)))
	}
	a.provider = p
	slog.Info("oidc: provider discovered", slog.String("issuer", a.config.IssuerURL))
	return p, nil
}

// Verifier returns a go-oidc IDTokenVerifier for the configured client ID.
func (a *Adapter) Verifier(ctx context.Context) (*gooidc.IDTokenVerifier, error) {
	p, err := a.Provider(ctx)
	if err != nil {
		return nil, err
	}
	return p.Verifier(&gooidc.Config{ClientID: a.config.ClientID}), nil
}

// Checkers returns a readyz probe for the OIDC provider. The probe verifies
// that the cached provider is populated without re-discovering.
//
// ref: kubernetes/kubernetes pkg/util/healthz — named health checkers.
func (a *Adapter) Checkers() map[string]func(context.Context) error {
	return adapterutil.HealthToCheckers("oidc_ready", a.healthProbe, adapterutil.DefaultProbeTimeout)
}

// Worker returns a worker.Worker that drives the periodic OIDC re-discovery
// loop. Bootstrap wires this via WorkerGroup so the loop starts with the
// service lifecycle.
//
// ref: adapters/s3.Client.Worker — same worker-adapter pattern.
func (a *Adapter) Worker() worker.Worker { return &oidcRefreshWorker{a: a} }

// signalStop closes stopCh exactly once (idempotent via sync.Once).
func (a *Adapter) signalStop() {
	a.stopOnce.Do(func() { close(a.stopCh) })
}

// Close implements lifecycle.ManagedResource. It signals the worker to stop
// and waits for the goroutine to drain, bounded by ctx. Idempotent — safe to
// call from both Worker.Stop and ManagedResource.Close teardown paths.
//
// Fast path: if the worker goroutine was never started (e.g. bootstrap aborted
// before Worker.Start was called), workerDone will never be closed. In that
// case we skip the select so we do not burn the shutdown budget on a
// non-existent drain.
//
// ref: adapters/s3.Client.Close — same idempotent + ctx-bounded pattern.
func (a *Adapter) Close(ctx context.Context) error {
	a.signalStop()
	// Fast path: worker goroutine never started — nothing to drain.
	if !a.started.Load() {
		return nil
	}
	select {
	case <-a.workerDone:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// healthProbe verifies the cached provider is populated. It does NOT
// re-discover — Refresh() handles that. Provider(ctx) acquires a.mu.RLock();
// the probe's inner 5s timeout (adapterutil.DefaultProbeTimeout) bounds the
// worst case if the lock is briefly held by a concurrent Refresh().
func (a *Adapter) healthProbe(ctx context.Context) error {
	_, err := a.Provider(ctx)
	return err
}

// OAuth2Config returns an oauth2.Config using the provider's endpoints.
func (a *Adapter) OAuth2Config(ctx context.Context) (*oauth2.Config, error) {
	p, err := a.Provider(ctx)
	if err != nil {
		return nil, err
	}
	scopes := a.config.Scopes
	if len(scopes) == 0 {
		scopes = []string{gooidc.ScopeOpenID, "profile", "email"}
	}
	return &oauth2.Config{
		ClientID:     a.config.ClientID,
		ClientSecret: a.config.ClientSecret,
		Endpoint:     p.Endpoint(),
		RedirectURL:  a.config.RedirectURL,
		Scopes:       scopes,
	}, nil
}
