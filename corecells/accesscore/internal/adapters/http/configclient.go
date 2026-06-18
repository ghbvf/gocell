// Package http provides HTTP adapter implementations for accesscore's outbound
// cross-cell calls.
package http

import (
	"context"
	"fmt"
	"net/http"
	"time"

	getclient "github.com/ghbvf/gocell/generated/contracts/http/config/internalapi/get/v1"

	"github.com/ghbvf/gocell/corecells/accesscore/internal/ports"
	kauth "github.com/ghbvf/gocell/framework/kernel/auth"
	"github.com/ghbvf/gocell/framework/kernel/clock"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/pkg/tenant"
	"github.com/ghbvf/gocell/framework/runtime/transport"
)

const (
	internalKeyQuotedFmt = "key=%q"

	// configClientTimeout bounds a single config refetch so a long-lived caller
	// ctx (e.g. an event-consumer ctx) cannot let an in-process/remote configcore
	// call hang indefinitely. This is a caller-policy concern (the generated client
	// imposes no timeout), so it stays in this adapter.
	configClientTimeout = 5 * time.Second
)

// HTTPConfigGetter calls configcore's internal GET /internal/v1/config/{key}
// contract through the generated, sealed cross-cell client
// ([getclient.Client], #2093) — the sole sibling-cell call type. The generated
// client holds the injected [transport.CellTransport] seam (in-proc short-circuit
// or remote, US5 #1966), signs every request with accesscore's per-cell service
// subkey (#2153), dispatches via DoContract, and decodes the success body. This
// adapter owns only the configcore-specific mapping: HTTP status → domain errcode
// and the generated DTO → [ports.ConfigEntry].
//
// contract: http.config.internal.get.v1
// ref: go-micro config/source/remote — polling + on-change patterns.
type HTTPConfigGetter struct {
	client *getclient.Client
}

// NewHTTPConfigGetter creates a new HTTPConfigGetter. t is the cross-cell sync
// transport (the composition root injects the in-process or remote impl chosen
// from the deployment topology); ring signs the service-token Authorization
// header. Both are forwarded to the generated client, whose constructor fails
// fast on a nil/typed-nil transport or nil ring and runs clock.MustHaveClock.
func NewHTTPConfigGetter(t transport.CellTransport, ring kauth.ServiceKeyring, clk clock.Clock) *HTTPConfigGetter {
	// callerCell="accesscore" mirrors contract.yaml endpoints.clients[0].
	return &HTTPConfigGetter{client: getclient.NewClient(t, ring, "accesscore", clk)}
}

// GetEntry fetches the current config entry for key from the configcore
// internal endpoint. t is forwarded as X-Tenant-ID (by the generated client's
// SignInternalRequest) so configcore's RLS scopes the lookup to the caller's real
// tenant tier. Returns errcode.ErrConfigRepoNotFound when the key does not exist
// in that tier (HTTP 404).
func (c *HTTPConfigGetter) GetEntry(ctx context.Context, t tenant.TenantID, key string) (ports.ConfigEntry, error) {
	// Bound the single dispatch: a long-lived caller ctx (event-consumer ctx) must
	// not let a configcore call hang indefinitely (the generated client imposes no
	// timeout; this caller-policy bound stays here).
	ctx, cancel := context.WithTimeout(ctx, configClientTimeout)
	defer cancel()

	rd, status, err := c.client.Get(ctx, t, &getclient.Request{Key: key})
	if err != nil {
		// Transport / build / decode error — already wrapped by the generated client.
		return ports.ConfigEntry{}, err
	}

	switch status {
	case http.StatusOK:
		// rd is non-nil on the success status; fall through to map.
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
				errcode.InternalAttr("status", status),
				errcode.InternalAttr("key", key),
			))
	}

	return ports.ConfigEntry{
		Key:       rd.Key,
		Value:     rd.Value,
		Sensitive: rd.Sensitive,
		Version:   int(rd.Version),
	}, nil
}

// Ensure HTTPConfigGetter implements ports.ConfigGetter at compile time.
var _ ports.ConfigGetter = (*HTTPConfigGetter)(nil)
