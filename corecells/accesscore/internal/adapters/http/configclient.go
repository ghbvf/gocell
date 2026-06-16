// Package http provides HTTP adapter implementations for accesscore's outbound
// cross-cell calls.
package http

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	"github.com/ghbvf/gocell/corecells/accesscore/internal/ports"
	kauth "github.com/ghbvf/gocell/framework/kernel/auth"
	"github.com/ghbvf/gocell/framework/kernel/clock"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/pkg/panicregister"
	"github.com/ghbvf/gocell/framework/pkg/tenant"
	"github.com/ghbvf/gocell/framework/pkg/validation"
	"github.com/ghbvf/gocell/framework/runtime/auth"
	"github.com/ghbvf/gocell/framework/runtime/transport"
)

const (
	internalKeyQuotedFmt = "key=%q"

	// configInternalGetContractID is the logical contract dispatched through the
	// CellTransport seam — in-process it routes by request path, remotely (US5
	// #1966) it resolves configcore's endpoint.
	configInternalGetContractID = "http.config.internal.get.v1"

	// configClientTimeout bounds a single config refetch so a long-lived caller
	// ctx (e.g. an event-consumer ctx) cannot let an in-process/remote configcore
	// call hang indefinitely. Restores the 5s bound the pre-transport *http.Client
	// carried. MESSAGE-CONST-LITERAL-01 messages below are required-dep fail-fasts.
	configClientTimeout = 5 * time.Second

	msgTransportNil = "accesscore/http.NewHTTPConfigGetter: CellTransport is required (nil rejected); " +
		"the composition root must inject the in-process or remote transport"
	msgRingNil = "accesscore/http.NewHTTPConfigGetter: ServiceKeyring is required (nil rejected); " +
		"the composition root must supply InternalServiceKeyring"
)

// configEntryDataResponse mirrors the {data: {...}} envelope returned by
// GET /internal/v1/config/{key} (contract: http.config.internal.get.v1).
type configEntryDataResponse struct {
	Data struct {
		Key       string `json:"key"`
		Value     string `json:"value"`
		Sensitive bool   `json:"sensitive"`
		Version   int    `json:"version"`
	} `json:"data"`
}

// HTTPConfigGetter calls configcore's internal GET /internal/v1/config/{key}
// contract through the injected [transport.CellTransport] seam. It signs every
// outbound request with a service token keyed by accesscore's per-cell subkey
// (resolved from the provided ServiceKeyring, #2153), then hands the signed
// request to the transport — co-located it short-circuits in process (no loopback
// TCP), split it dials configcore remotely (US5 #1966). The transport never
// bypasses the auth chain, so signing stays here.
//
// contract: http.config.internal.get.v1
// ref: go-micro config/source/remote — polling + on-change patterns.
type HTTPConfigGetter struct {
	transport transport.CellTransport
	ring      kauth.ServiceKeyring
	clock     clock.Clock
}

// NewHTTPConfigGetter creates a new HTTPConfigGetter. t is the cross-cell sync
// transport (the composition root injects the in-process or remote impl chosen
// from the deployment topology); ring signs the service-token Authorization
// header.
func NewHTTPConfigGetter(t transport.CellTransport, ring kauth.ServiceKeyring, clk clock.Clock) *HTTPConfigGetter {
	clock.MustHaveClock(clk, "accesscore/http.NewHTTPConfigGetter")
	// Strong deps fail-fast at construction (programmer/wiring error), not at the
	// first request: a nil/typed-nil transport or a nil keyring is unrecoverable.
	if validation.IsNilInterface(t) {
		panic(panicregister.Approved("configgetter-transport-nil", errcode.Assertion(msgTransportNil)))
	}
	if validation.IsNilInterface(ring) {
		panic(panicregister.Approved("configgetter-ring-nil", errcode.Assertion(msgRingNil)))
	}
	return &HTTPConfigGetter{
		transport: t,
		ring:      ring,
		clock:     clk,
	}
}

// GetEntry fetches the current config entry for key from the configcore
// internal endpoint. t is forwarded as X-Tenant-ID so configcore's RLS scopes
// the lookup to the caller's real tenant tier. Returns
// errcode.ErrConfigRepoNotFound when the key does not exist in that tier
// (HTTP 404).
func (c *HTTPConfigGetter) GetEntry(ctx context.Context, t tenant.TenantID, key string) (ports.ConfigEntry, error) {
	// Bound the single dispatch: a long-lived caller ctx (event-consumer ctx) must
	// not let a configcore call hang indefinitely (restores the pre-transport 5s
	// *http.Client timeout, now transport-agnostic).
	ctx, cancel := context.WithTimeout(ctx, configClientTimeout)
	defer cancel()

	path := "/internal/v1/config/" + url.PathEscape(key)

	// The request carries only the contract path; the transport places it
	// (in-process: routes by path against the internal handler; remote: fills
	// configcore's host). The signed token below covers this same path.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, path, nil)
	if err != nil {
		return ports.ConfigEntry{}, fmt.Errorf("configclient: build request: %w", err)
	}

	// Sign the request with a service token so the InternalListener middleware accepts it.
	// callerCell="accesscore" mirrors contract.yaml endpoints.clients[0]. The tenant t and
	// any propagated business principal are folded into the MAC so tamper/inject/strip of
	// either signed header fails verification at the receiving end.
	if err := auth.SignInternalRequest(ctx, c.ring, "accesscore", req, t, c.clock); err != nil {
		return ports.ConfigEntry{}, err
	}

	resp, err := c.transport.DoContract(ctx, configInternalGetContractID, req)
	if err != nil {
		return ports.ConfigEntry{}, fmt.Errorf("configclient: do request: %w", err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			slog.Warn("configclient: response body close error", slog.Any("error", err))
		}
	}()

	switch resp.StatusCode {
	case http.StatusOK:
		// fall through to decode
	case http.StatusNotFound:
		return ports.ConfigEntry{}, errcode.New(errcode.KindNotFound, errcode.ErrConfigRepoNotFound,
			"config key not found",
			errcode.WithCategory(errcode.CategoryDomain),
			errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf(internalKeyQuotedFmt, key))))
	case http.StatusUnauthorized:
		// Permanent: invalid/missing/tampered service token. Retrying with the
		// same credentials cannot recover; consumers must Reject (DLQ) rather
		// than treat as transient.
		return ports.ConfigEntry{}, errcode.New(errcode.KindUnauthenticated, errcode.ErrAuthUnauthorized,
			"configclient: 401 from configcore (service token rejected)",
			errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf(internalKeyQuotedFmt, key))))
	case http.StatusForbidden:
		// Permanent: caller_cell not in contract.clients allowlist. Retrying
		// with the same caller cannot recover; consumers must Reject (DLQ).
		return ports.ConfigEntry{}, errcode.New(errcode.KindPermissionDenied, errcode.ErrAuthForbidden,
			"configclient: 403 from configcore (caller_cell not in allowlist)",
			errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf(internalKeyQuotedFmt, key))))
	case http.StatusBadRequest:
		// Permanent: the X-Tenant-ID header was absent, malformed, or a
		// nil-UUID. Retrying with the same (broken) context cannot recover;
		// consumers must Reject (DLQ) rather than burning retry budget.
		return ports.ConfigEntry{}, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"configclient: 400 from configcore (invalid X-Tenant-ID)",
			errcode.WithInternal(errcode.InternalAttr("key", key)))
	default:
		// Unexpected status (e.g. 5xx). Keep the status + key on the server-only
		// Internal channel; the message is a const literal (MESSAGE-CONST-LITERAL-01).
		return ports.ConfigEntry{}, errcode.New(errcode.KindUnavailable, errcode.ErrServiceUnavailable,
			"configclient: unexpected status from configcore",
			errcode.WithInternal(
				errcode.InternalAttr("status", resp.StatusCode),
				errcode.InternalAttr("key", key),
			))
	}

	var env configEntryDataResponse
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		return ports.ConfigEntry{}, fmt.Errorf("configclient: decode response: %w", err)
	}

	return ports.ConfigEntry{
		Key:       env.Data.Key,
		Value:     env.Data.Value,
		Sensitive: env.Data.Sensitive,
		Version:   env.Data.Version,
	}, nil
}

// Ensure HTTPConfigGetter implements ports.ConfigGetter at compile time.
var _ ports.ConfigGetter = (*HTTPConfigGetter)(nil)
