package devtools

import (
	"context"
	"log/slog"
	"net/http"
	"strings"

	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/metadata"
	"github.com/ghbvf/gocell/kernel/wrapper"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/httputil"
	"github.com/ghbvf/gocell/runtime/auth"
)

// roleAdmin gates all devtools catalog routes.
//
// Keep in sync with cells/accesscore/internal/domain.RoleAdmin (cell-isolation
// rules forbid runtime/ from importing internal/ packages, so each consumer
// holds a manually-synced copy of the role string).
const roleAdmin = "admin"

// specCatalog is the framework-internal ContractSpec for the devtools catalog
// endpoint. The "http.framework.devtools." prefix exempts it from FMT-18
// contract-yaml presence validation because it lives in runtime/, not cells/.
//
// Note: catalog responses use the Backstage Catalog Entity envelope at top
// level (apiVersion/kind/metadata/spec). They do NOT wrap in {"data": ...}
// per api-versioning.md — that envelope rule applies to cell-owned business
// routes; framework-internal routes (this + /healthz /readyz /metrics) follow
// their own wire formats.
var specCatalog = wrapper.ContractSpec{
	ID:        "http.framework.devtools.catalog.v1",
	Kind:      "http",
	Transport: "http",
	Method:    "GET",
	Path:      "/api/v1/devtools/catalog",
}

// validKinds is the whitelist for the ?kinds= query parameter.
// Derived from metadata.AllKinds — single source of truth.
var validKinds = sliceToSet(metadata.AllKinds)

// validLayers is the whitelist for the ?layers= query parameter.
// Derived from metadata.AllLayers — single source of truth.
var validLayers = sliceToSet(metadata.AllLayers)

// sliceToSet converts a string slice to a map[string]bool for O(1) lookup.
func sliceToSet(vals []string) map[string]bool {
	m := make(map[string]bool, len(vals))
	for _, v := range vals {
		m[v] = true
	}
	return m
}

// Handler serves the devtools catalog HTTP endpoint.
type Handler struct {
	project   *metadata.ProjectMeta
	cellGraph *metadata.CellDepGraph
	pkgLoader *PackageDepLoader
	root      string
	clock     clock.Clock
}

// NewHandler constructs a Handler. cellGraph may be nil (omits cell dep graph).
// pkgLoader may be nil; when nil the package-deps block is omitted from output.
func NewHandler(
	project *metadata.ProjectMeta,
	cellGraph *metadata.CellDepGraph,
	pkgLoader *PackageDepLoader,
	root string,
	clk clock.Clock,
) *Handler {
	return &Handler{
		project:   project,
		cellGraph: cellGraph,
		pkgLoader: pkgLoader,
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
				Policy:   auth.AnyRole(roleAdmin),
			})
		},
	}
}

// ServeHTTP handles GET /api/v1/devtools/catalog.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Parse and validate query parameters.
	filter, format, ok := parseQuery(ctx, w, r)
	if !ok {
		return
	}

	// Build export options.
	opts := buildExportOptions(h, filter)

	// Build document.
	doc, err := metadata.BuildDocument(h.project, opts)
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
	body, err := metadata.MarshalDocument(doc, format)
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
func buildExportOptions(h *Handler, filter metadata.Filter) metadata.ExportOptions {
	opts := metadata.ExportOptions{
		Now:    h.clock.Now(),
		Root:   ".",
		Filter: filter,
	}
	if filter.Include&metadata.IncludeCellDeps != 0 {
		opts.CellDeps = h.cellGraph
	}
	if filter.Include&metadata.IncludePackageDeps != 0 && h.pkgLoader != nil {
		opts.Packages = h.pkgLoader.View()
	}
	return opts
}

// parseQuery extracts and validates query parameters from r. On any validation
// error it writes a 400 response and returns false.
func parseQuery(ctx context.Context, w http.ResponseWriter, r *http.Request) (metadata.Filter, string, bool) {
	q := r.URL.Query()

	kinds, ok := parseCSVWhitelist(ctx, w, q.Get("kinds"), validKinds, "kinds")
	if !ok {
		return metadata.Filter{}, "", false
	}

	layers, ok := parseCSVWhitelist(ctx, w, q.Get("layers"), validLayers, "layers")
	if !ok {
		return metadata.Filter{}, "", false
	}

	cells := parseCells(q.Get("cells"))

	// Distinguish "omitted" (?include absent → IncludeAll) from "explicit empty"
	// (?include= → 0). q.Has returns true only when the key appears in the query
	// string, even with an empty value.
	includeRaw := ""
	includePresent := q.Has("include")
	if includePresent {
		includeRaw = q.Get("include")
	}
	include, ok := parseInclude(ctx, w, includeRaw, includePresent)
	if !ok {
		return metadata.Filter{}, "", false
	}

	format := q.Get("format")
	if format == "" {
		format = "json"
	}
	if format != "json" && format != "yaml" {
		httputil.WritePublicError(ctx, w, http.StatusBadRequest,
			string(errcode.ErrValidationFailed),
			"unknown format value: "+format)
		return metadata.Filter{}, "", false
	}

	return metadata.Filter{
		Kinds:   kinds,
		Layers:  layers,
		Cells:   cells,
		Include: include,
	}, format, true
}

// parseCSVWhitelist splits a comma-separated string, trims whitespace,
// deduplicates, and validates each token against allowedSet.
func parseCSVWhitelist(
	ctx context.Context,
	w http.ResponseWriter,
	raw string,
	allowedSet map[string]bool,
	paramName string,
) ([]string, bool) {
	if raw == "" {
		return nil, true
	}
	parts := strings.Split(raw, ",")
	seen := make(map[string]bool, len(parts))
	result := make([]string, 0, len(parts))
	for _, p := range parts {
		v := strings.TrimSpace(p)
		if v == "" {
			continue
		}
		if !allowedSet[v] {
			httputil.WritePublicError(ctx, w, http.StatusBadRequest,
				string(errcode.ErrValidationFailed),
				"unknown "+paramName+" value: "+v)
			return nil, false
		}
		if !seen[v] {
			seen[v] = true
			result = append(result, v)
		}
	}
	return result, true
}

// parseCells splits a comma-separated cell ID list, deduplicating.
func parseCells(raw string) []string {
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	result := make([]string, 0, len(parts))
	seen := make(map[string]bool, len(parts))
	for _, p := range parts {
		v := strings.TrimSpace(p)
		if v != "" && !seen[v] {
			seen[v] = true
			result = append(result, v)
		}
	}
	return result
}

// parseInclude parses the ?include= parameter into an IncludeMask.
// When present is false (parameter absent), returns IncludeAll.
// When present is true and raw is empty, returns 0 (no optional blocks).
// Unknown values write a 400 response and return false.
func parseInclude(ctx context.Context, w http.ResponseWriter, raw string, present bool) (metadata.IncludeMask, bool) {
	if !present {
		return metadata.IncludeAll, true
	}
	if raw == "" {
		return 0, true
	}
	parts := strings.Split(raw, ",")
	var mask metadata.IncludeMask
	for _, p := range parts {
		v := strings.TrimSpace(p)
		if v == "" {
			continue
		}
		bit, ok := includeBit(v)
		if !ok {
			httputil.WritePublicError(ctx, w, http.StatusBadRequest,
				string(errcode.ErrValidationFailed),
				"unknown include value: "+v)
			return 0, false
		}
		mask |= bit
	}
	return mask, true
}

// includeBit maps an include token name to its IncludeMask bit.
func includeBit(s string) (metadata.IncludeMask, bool) {
	switch s {
	case "relations":
		return metadata.IncludeRelations, true
	case "statusBoard":
		return metadata.IncludeStatusBoard, true
	case "cellDeps":
		return metadata.IncludeCellDeps, true
	case "packageDeps":
		return metadata.IncludePackageDeps, true
	default:
		return 0, false
	}
}

// contentType returns the Content-Type header value for the given format.
func contentType(format string) string {
	if format == "yaml" {
		return "application/yaml; charset=utf-8"
	}
	return "application/json; charset=utf-8"
}

// writeBody writes body bytes to w, logging any write error.
// Extracted to satisfy gosec G705 (XSS via taint analysis) — the taint
// originates in metadata.MarshalDocument which produces deterministic,
// admin-only output; the nolint annotation is intentional.
func writeBody(w http.ResponseWriter, body []byte) {
	if _, err := w.Write(body); err != nil { //nolint:gosec // body is serialized catalog metadata, admin-only endpoint
		slog.Error("devtools: write response body", slog.Any("error", err))
	}
}
