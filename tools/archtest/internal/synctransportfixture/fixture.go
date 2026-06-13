//go:build archtest_fixture

// Package synctransportfixture is the CELL-SYNC-TRANSPORT-FUNNEL-01 negative
// fixture. It stages the forbidden raw-HTTP-client forms a cell must never use to
// dial a sibling — CONSTRUCTING a *http.Client, HOLDING one as state, using the
// http.Get convenience func, and referencing http.DefaultClient. It also stages
// the legitimate GREEN surface (request shaping via http.NewRequestWithContext +
// dispatch through the sanctioned transport.CellTransport.DoContract) that must
// NOT fire — proving the scan distinguishes the client/dispatch surface from
// request shaping + the transport seam by SYMBOL, not by import (a cell
// legitimately imports net/http for requests).
//
// Loaded only under the archtest_fixture build tag (via Run(t, Fixture(...)));
// never imported from production code. It imports the real runtime/transport
// package so the GREEN DoContract path has real AST coverage.
//
// DO NOT use this package in production code.
package synctransportfixture

import (
	"context"
	"net/http"

	"github.com/ghbvf/gocell/runtime/transport"
)

// dialSibling constructs a raw *http.Client and dispatches — the forbidden chokepoint.
func dialSibling() (*http.Response, error) {
	c := &http.Client{} // VIOLATION [client-type]
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "/internal/v1/config/x", nil)
	return c.Do(req)
}

// heldClient holds a raw *http.Client as cell state — a cell retaining a
// cross-cell dial client.
type heldClient struct {
	client *http.Client // VIOLATION [client-type]
}

// convenienceDial uses the package-level convenience func (dispatches via
// http.DefaultClient) — forbidden.
func convenienceDial() (*http.Response, error) {
	return http.Get("http://sibling/internal/v1/config/x") // VIOLATION [convenience]
}

// defaultClientUse references http.DefaultClient directly — forbidden.
func defaultClientUse(req *http.Request) (*http.Response, error) {
	return http.DefaultClient.Do(req) // VIOLATION [default-client]
}

// goodDispatch routes through the sanctioned transport.CellTransport — GREEN. It
// shapes a request (http.NewRequestWithContext, legitimate) and hands it to the
// injected transport (DoContract), which must NOT fire.
func goodDispatch(ct transport.CellTransport) (*http.Response, error) {
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "/internal/v1/config/x", nil) // GREEN (request shaping)
	return ct.DoContract(context.Background(), "http.config.internal.get.v1", req)                           // GREEN (sanctioned path)
}

// keep the held type referenced so the fixture is a single connected unit.
var _ = heldClient{}
