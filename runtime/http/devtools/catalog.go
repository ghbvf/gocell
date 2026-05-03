package devtools

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/clock"
	kerneldepgraph "github.com/ghbvf/gocell/kernel/depgraph"
	"github.com/ghbvf/gocell/kernel/metadata"
	"github.com/ghbvf/gocell/kernel/wrapper"
	"github.com/ghbvf/gocell/pkg/csvparam"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/httputil"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/ghbvf/gocell/runtime/devtools/catalog"
)

// specCatalog is the framework-internal ContractSpec for the devtools catalog
// endpoint. The "http.framework.devtools." prefix exempts it from FMT-18
// contract-yaml presence validation because it lives in runtime/, not cells/.
//
// Note: catalog responses use the Backstage Catalog Entity envelope at top
// level (apiVersion/kind/metadata/spec). They do NOT wrap in {"data": ...}
// per api-versioning.md — that envelope rule applies to cell-owned business
// routes; framework-internal routes follow their own wire formats.
var specCatalog = wrapper.ContractSpec{
	ID:        "http.framework.devtools.catalog.v1",
	Kind:      "http",
	Transport: "http",
	Method:    "GET",
	Path:      "/api/v1/devtools/catalog",
}

// validIncludeTokens is the set of accepted ?include= tokens.
var validIncludeTokens = []string{"cellDeps", "packageDeps", "relations", "statusBoard"}

// Handler serves the devtools catalog HTTP endpoint.
type Handler struct {
	project   *metadata.ProjectMeta
	cellGraph *catalog.CellDepGraph
	pkgGraph  *kerneldepgraph.Graph
	root      string
	clock     clock.Clock
}

// NewHandler constructs a Handler. cellGraph may be nil (omits cell dep graph).
// pkgGraph may be nil; when nil the package-deps block is omitted from output.
// pkgGraph is the build-time generated graph from cmd/corebundle/catalog_gen.go.
func NewHandler(
	project *metadata.ProjectMeta,
	cellGraph *catalog.CellDepGraph,
	pkgGraph *kerneldepgraph.Graph,
	root string,
	clk clock.Clock,
) *Handler {
	return &Handler{
		project:   project,
		cellGraph: cellGraph,
		pkgGraph:  pkgGraph,
		root:      root,
		clock:     clk,
	}
}

// RouteGroup returns the cell.RouteGroup that bootstrap mounts on PrimaryListener.
func RouteGroup(h *Handler) cell.RouteGroup {
	return cell.RouteGroup{
		Listener: cell.PrimaryListener,
		Register: func(mux cell.RouteMux) error {
			return auth.Mount(mux, auth.Route{
				Contract: specCatalog,
				Handler:  http.HandlerFunc(h.ServeHTTP),
				Policy:   auth.AnyRole(auth.RoleAdmin),
			})
		},
	}
}

// ServeHTTP handles GET /api/v1/devtools/catalog.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Parse and validate query parameters.
	filter, format, ok := parseQuery(ctx, w, r, h.knownCellIDs())
	if !ok {
		return
	}

	// Build export options.
	opts := buildExportOptions(h, filter)

	// Build document.
	doc, err := catalog.BuildDocument(h.project, opts)
	if err != nil {
		slog.Error("devtools: BuildDocument failed",
			slog.Any("error", err),
			slog.String("root", h.root),
		)
		httputil.WritePublicError(ctx, w, http.StatusInternalServerError,
			string(errcode.ErrInternal), "internal server error")
		return
	}

	// Marshal document.
	body, err := catalog.MarshalDocument(doc, format)
	if err != nil {
		// format is validated above to be "json" or "yaml" — not user-controlled.
		slog.Error("devtools: MarshalDocument failed", //nolint:gosec // format validated to "json"|"yaml" in parseQuery before reaching here
			slog.String("format", format),
			slog.Any("error", err),
		)
		httputil.WritePublicError(ctx, w, http.StatusInternalServerError,
			string(errcode.ErrInternal), "internal server error")
		return
	}

	// Write response.
	w.Header().Set("Content-Type", contentType(format))
	w.WriteHeader(http.StatusOK)
	writeBody(w, body)
}

// buildExportOptions assembles ExportOptions from the handler state and filter.
// Root is always set to "." for HTTP responses to avoid leaking absolute server
// paths to clients; CLI callers retain their absolute path via h.root.
func buildExportOptions(h *Handler, filter catalog.Filter) catalog.ExportOptions {
	opts := catalog.ExportOptions{
		Clock:  h.clock,
		Root:   ".",
		Filter: filter,
	}
	if filter.Include.CellDeps {
		opts.CellDeps = h.cellGraph
	}
	if filter.Include.PackageDeps && h.pkgGraph != nil {
		opts.Packages = &catalog.PackageDepsView{Graph: h.pkgGraph}
	}
	return opts
}

// knownCellIDs returns the set of cell IDs the project declares. Used as the
// allowlist for the ?cells= query parameter so unknown IDs are rejected at the
// edge instead of leaking existence info via response shape differences.
func (h *Handler) knownCellIDs() []string {
	if h.project == nil {
		return nil
	}
	ids := make([]string, 0, len(h.project.Cells))
	for _, c := range h.project.Cells {
		ids = append(ids, c.ID)
	}
	return ids
}

// parseQuery extracts and validates query parameters from r. On any validation
// error it writes a 400 response and returns false.
func parseQuery(ctx context.Context, w http.ResponseWriter, r *http.Request, knownCells []string) (catalog.Filter, string, bool) {
	q := r.URL.Query()

	kinds, err := csvparam.ParseAllowed(q.Get("kinds"), catalog.AllKinds, "kinds")
	if err != nil {
		writeValidationError(ctx, w, err.Error())
		return catalog.Filter{}, "", false
	}

	layers, err := csvparam.ParseAllowed(q.Get("layers"), catalog.AllLayers, "layers")
	if err != nil {
		writeValidationError(ctx, w, err.Error())
		return catalog.Filter{}, "", false
	}

	cells, err := csvparam.ParseAllowed(q.Get("cells"), knownCells, "cells")
	if err != nil {
		writeValidationError(ctx, w, err.Error())
		return catalog.Filter{}, "", false
	}

	// Distinguish "omitted" (?include absent → AllIncluded) from "explicit empty"
	// (?include= → zero IncludeOptions). q.Has returns true only when the key
	// appears in the query string, even with an empty value.
	includeRaw := ""
	includePresent := q.Has("include")
	if includePresent {
		includeRaw = q.Get("include")
	}
	inc, ok := parseInclude(ctx, w, includeRaw, includePresent)
	if !ok {
		return catalog.Filter{}, "", false
	}

	format := q.Get("format")
	if format == "" {
		format = "json"
	}
	if format != "json" && format != "yaml" {
		writeValidationError(ctx, w, "invalid format parameter")
		return catalog.Filter{}, "", false
	}

	return catalog.Filter{
		Kinds:   kinds,
		Layers:  layers,
		Cells:   cells,
		Include: inc,
	}, format, true
}

// parseInclude parses the ?include= parameter into an IncludeOptions.
// When present is false (parameter absent), returns AllIncluded().
// When present is true and raw is empty, returns zero IncludeOptions.
// Unknown values write a 400 response and return false.
func parseInclude(ctx context.Context, w http.ResponseWriter, raw string, present bool) (catalog.IncludeOptions, bool) {
	if !present {
		return catalog.AllIncluded(), true
	}
	tokens, err := csvparam.ParseAllowed(raw, validIncludeTokens, "include")
	if err != nil {
		writeValidationError(ctx, w, err.Error())
		return catalog.IncludeOptions{}, false
	}
	var inc catalog.IncludeOptions
	for _, token := range tokens {
		switch token {
		case "cellDeps":
			inc.CellDeps = true
		case "packageDeps":
			inc.PackageDeps = true
		case "relations":
			inc.Relations = true
		case "statusBoard":
			inc.StatusBoard = true
		default:
			writeValidationError(ctx, w, csvparam.UnknownTokenError{
				Param:   "include",
				Allowed: validIncludeTokens,
			}.Error())
			return catalog.IncludeOptions{}, false
		}
	}
	return inc, true
}

func writeValidationError(ctx context.Context, w http.ResponseWriter, message string) {
	httputil.WritePublicError(ctx, w, http.StatusBadRequest,
		string(errcode.ErrValidationFailed), message)
}

// contentType returns the Content-Type header value for the given format.
func contentType(format string) string {
	if format == "yaml" {
		return "application/yaml"
	}
	return "application/json; charset=utf-8"
}

// writeBody writes body bytes to w, logging any write error.
// Extracted to satisfy gosec G705 (XSS via taint analysis) — the taint
// originates in catalog.MarshalDocument which produces deterministic,
// admin-only output; the nolint annotation is intentional.
func writeBody(w http.ResponseWriter, body []byte) {
	if _, err := w.Write(body); err != nil { //nolint:gosec // body is serialized catalog metadata, admin-only endpoint
		slog.Error("devtools: write response body", slog.Any("error", err))
	}
}
