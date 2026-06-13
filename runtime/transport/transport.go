package transport

import (
	"context"
	"net/http"
)

// CellTransport is the location-transparent sync (http) contract transport seam
// (ADR D2). A cell's generated/hand-written contract client holds a CellTransport
// and dispatches every cross-cell http call through DoContract; the composition
// root injects the implementation chosen from the deployment topology
// (in-process short-circuit when co-located, remote HTTP client when split —
// US5 #1966).
//
// The signature is contract-level HTTP and is FROZEN by ADR D2. contractID is the
// logical target contract (e.g. "http.config.internal.get.v1") used for
// observability and, in the remote impl, for cellID→endpoint resolution; the
// request carries the method/path/headers/body (the caller signs it with a
// service token before calling — signing stays caller-side). The seam covers
// only the http kind.
type CellTransport interface {
	DoContract(ctx context.Context, contractID string, req *http.Request) (*http.Response, error)
}

// TransportMode is the sealed, binary observability dimension distinguishing an
// in-process short-circuit from a remote network call (ADR D4). The value set is
// closed by the type system: the single field is unexported and the only
// constructors are the package-private singletons exposed via [ModeInProc] /
// [ModeRemote], so an external package cannot mint a third mode or pass a raw
// string where a TransportMode is required. The two-value set is intentionally
// low-cardinality, so it is a legitimate frozen metric label (not a trace-only
// attribute).
type TransportMode struct {
	// v is the wire/label value ("in_proc" | "remote"). Unexported: a non-zero
	// TransportMode cannot be constructed outside this package.
	v string
}

// String returns the metric-label / span-attribute value.
func (m TransportMode) String() string { return m.v }

// Package-private singletons — the sole TransportMode values. Exposed via
// accessor functions (not exported vars) so the registered values are immutable:
// reassigning a function is a compile error.
var (
	modeInProc = TransportMode{v: "in_proc"}
	modeRemote = TransportMode{v: "remote"}
)

// ModeInProc is the transport mode for an in-process short-circuit dispatch.
func ModeInProc() TransportMode { return modeInProc }

// ModeRemote is the transport mode for a remote network call (US5 #1966).
func ModeRemote() TransportMode { return modeRemote }

// allTransportModes is the closed registry of every TransportMode value. It
// backs the anti-vacuity freeze (TestTransportMode_FrozenRegistry): a new mode
// added without registering it here — or a renamed wire value — is caught.
var allTransportModes = []TransportMode{modeInProc, modeRemote}
