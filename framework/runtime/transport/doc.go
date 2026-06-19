// Package transport is the framework's location-transparent sync (http) contract
// transport seam (Epic #1423 US4 #1963). It lets the SAME cell code reach a
// sibling cell's http-kind contract whether that cell is co-located in the same
// process (in-process short-circuit) or hosted remotely (US5 #1966), with the
// implementation chosen by the composition root from the deployment topology.
//
// # Seam (ADR D2)
//
// All cross-cell sync calls go through the [CellTransport] interface:
//
//	DoContract(ctx, contractID, *http.Request) (*http.Response, error)
//
// The interface is contract-level HTTP (a deliberate deviation from Service
// Weaver's method-level RPC stub — contract.yaml stays the explicit governance
// boundary for idempotency / delivery / error codes). The seam covers ONLY the
// `http` contract kind; `grpc` cross-cell relocatability is a separate future
// seam (the `*http.Request`/`*http.Response` signature only expresses HTTP).
//
// # In-process implementation (ADR D4)
//
// [InProcessTransport] holds the BUILT internal-listener http.Handler and
// dispatches a request against it in memory (httptest.ResponseRecorder +
// handler.ServeHTTP), replacing the loopback TCP hop. Crucially it does NOT
// bypass the listener auth chain: the dispatched request still flows through
// ServiceTokenMiddleware + RequireCallerCell exactly like a remote call. The
// caller signs the request with a real service token (signing stays caller-side);
// the transport only swaps the wire (TCP → ServeHTTP), never the governance stack.
//
// The handler only exists after bootstrap phase5, while the consumer cell is
// wired earlier — so the holder is constructed empty by the composition root,
// shared by reference with both the consumer (via composition.SharedDeps) and
// bootstrap, and the built handler is bound once (WriteOnce) at phase5. A
// DoContract before bind fails fast (a misordered-wiring programmer error), never
// a nil-handler panic.
//
// # Observability (ADR D4)
//
// Every call is tagged with a binary, frozen [TransportMode] ∈ {in_proc, remote}
// — on the metric series (`cell_transport_requests_total{transport_mode}`) and as
// a span attribute — so a "transparent" call never becomes "undiagnosable"
// (Service Weaver's 2024-12 lesson). The two-value set is closed by the sealed
// TransportMode type (an external package cannot mint a third value).
//
// # Governance
//
//   - Upstream Hard: [InProcessTransport] / [RemoteHTTPTransport] are sealed types
//     (unexported fields + single sanctioned constructors) — no package outside
//     can forge a transport. Field sets frozen by INPROCESS-TRANSPORT-SEALED-01 /
//     REMOTE-TRANSPORT-SEALED-01.
//   - Downstream Hard (#2093) — the GENERATED ARTIFACT, not the interface: the
//     codegen contract client is byte-frozen by codegen + golden, so you cannot
//     hand-add or alter a sibling-call client type, and one is emitted for exactly
//     the internal contracts declaring endpoints.clients (contractgen
//     shouldEmitClient). The Hard guarantee is the golden-pinned generated code, NOT
//     a sealed interface: [CellTransport] is a plain exported interface (contrast
//     [TransportMode], which IS sealed — unexported field + accessors), so "the
//     generated constructor only takes a CellTransport" is not a type-level seal —
//     it only means a bare *http.Client is not accepted by that constructor's
//     signature. The constructor also bakes the caller-cell identity: one
//     NewClientFor<Cell> per endpoints.clients cell (no free callerCell string), so
//     a wrong/forged caller is unexpressible at the call site (F2).
//   - Downstream "sole expressible path" — Medium (two permanent ceilings): the
//     guarantee that a cell reaches a sibling sync path ONLY via a generated client
//     is enforced by AST scan, not the type system, because net/http is a stdlib API
//     and DoContract is an exported method (neither can be made a compile error), and
//     the CellTransport interface is not sealed (an external package could implement
//     it — though to actually dial it would still hold net/http, caught below).
//     CELL-SYNC-TRANSPORT-FUNNEL-01 bans a cell from holding/constructing a raw
//     net/http client; CELL-TRANSPORT-DOCONTRACT-CALLER-01 bans a cell from calling
//     CellTransport.DoContract directly (only generated clients may).
//
// ref: ServiceWeaver/weaver internal/weaver/remoteweavelet.go (local/remote
// dispatch); go-micro selector/default.go (minimal Resolver+transport shape).
// ADR: docs/architecture/202606131142-1423-adr-cell-deployment-topology.md (D2/D4).
package transport
