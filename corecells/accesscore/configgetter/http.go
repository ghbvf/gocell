// Package configgetter wires accesscore ConfigGetter adapters.
package configgetter

import (
	accesscore "github.com/ghbvf/gocell/corecells/accesscore"
	accesshttp "github.com/ghbvf/gocell/corecells/accesscore/internal/adapters/http"
	kauth "github.com/ghbvf/gocell/framework/kernel/auth"
	"github.com/ghbvf/gocell/framework/kernel/clock"
	"github.com/ghbvf/gocell/framework/runtime/transport"
)

// WithTransport constructs a CellTransport-backed ConfigGetter and injects it
// into accesscore. The composition root owns the concrete transport choice
// (in-process when configcore is co-located, remote when split — US5 #1966); the
// accesscore root package only receives the resulting port implementation. ring
// is the per-cell ServiceKeyring (#2153) that signs the outbound service token
// with accesscore's own subkey (signing stays caller-side; the transport only
// carries the signed request).
//
// contract: http.config.internal.get.v1
// ref: go-micro config/source/remote — polling + on-change patterns.
func WithTransport(t transport.CellTransport, ring kauth.ServiceKeyring, clk clock.Clock) accesscore.Option {
	return accesscore.WithConfigGetter(accesshttp.NewHTTPConfigGetter(t, ring, clk))
}
