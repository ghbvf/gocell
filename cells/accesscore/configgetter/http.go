// Package configgetter wires accesscore ConfigGetter adapters.
package configgetter

import (
	accesscore "github.com/ghbvf/gocell/cells/accesscore"
	accesshttp "github.com/ghbvf/gocell/cells/accesscore/internal/adapters/http"
	"github.com/ghbvf/gocell/runtime/auth"
)

// WithHTTP constructs an HTTP-backed ConfigGetter and injects it into
// accesscore. The composition root owns the concrete adapter choice; the
// accesscore root package only receives the resulting port implementation.
//
// contract: http.config.internal.get.v1
// ref: go-micro config/source/remote — polling + on-change patterns.
func WithHTTP(baseURL string, ring *auth.HMACKeyRing) accesscore.Option {
	return accesscore.WithConfigGetter(accesshttp.NewHTTPConfigGetter(baseURL, ring))
}
