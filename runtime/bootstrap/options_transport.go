package bootstrap

// options_transport.go — With* option for the in-process CellTransport seam
// (Epic #1423 US4 #1963).
//
// ref: uber-go/fx app.go — Option pattern; each Option targets a single concern.

import (
	"github.com/ghbvf/gocell/runtime/transport"
)

// WithInProcessTransport registers the shared in-process CellTransport holder so
// phase5 binds the built internal-listener handler into it (WriteOnce). The
// composition root constructs the holder once and hands the SAME pointer to both
// the consumer cell (via SharedDeps) and this option, so co-located cross-cell
// sync calls dispatch in memory through the full auth chain. Omitting it leaves
// the holder unbound; a consumer that still calls DoContract then fail-fasts
// (never a silent nil-handler panic).
func WithInProcessTransport(t *transport.InProcessTransport) Option {
	return func(b *Bootstrap) { b.inProcessTransport = t }
}
