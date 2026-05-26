package errcode

import (
	"encoding/json"
	"log/slog"
)

// PublicDetail is the sealed wire-safe key/value attribute carried by
// errcode.Error.Details. Construction is exclusive to PublicAttr; unexported
// fields make outside-package struct-literal construction inexpressible in
// Go's type system, replacing the prior Medium archtest DETAILS-SLOG-ATTR-01
// with a Hard type-system invariant (see
// .claude/rules/gocell/ai-robust.md §Hard 范本目录 "sealed construction").
//
// Wire schema is {"key": string, "value": any}, preserved across the
// refactor via MarshalJSON below; the JSON shape is the single source of
// truth for error.details entries
// (contracts/shared/errors/error-response-v1.schema.json).
//
// Known bypass surface: errcode.Error.Details is exported as []PublicDetail.
// External code can overwrite or append the slice with values constructed
// via PublicAttr. The wire-side 5xx Details-strip invariant
// (Error.MarshalJSON → project() → []PublicDetail{}) is the defense for
// leakage regardless of append source; the sealed-construction Hard claim
// is "no outside code can construct a non-zero PublicDetail without going
// through PublicAttr", not "no outside code can mutate Error.Details".
// Callers should still route input through WithDetails(...PublicDetail).
type PublicDetail struct {
	key   string
	value any
}

// PublicAttr constructs a PublicDetail with the given key and value.
//
// Value type guidance — prefer primitive JSON types:
//
//   - string, int / int64 / uint64, float64 (finite), bool — serialize verbatim.
//   - time.Time — serializes as an RFC3339Nano string.
//   - time.Duration — serializes as integer nanoseconds; prefer formatting
//     it as a human-readable string at the call site when surfacing
//     duration to operators.
//
// Non-JSON-marshalable values (channels, functions, NaN/Inf floats, cyclic
// references) surface as json.Marshal errors when the enclosing
// errcode.Error is serialized. The HTTP error-response writer
// (pkg/httputil.writeErrorBody) falls back to sentinelInternalErrorBody —
// HTTP 500 + the canonical {"error":{...}} envelope — so an unintended
// value type silently downgrades a 4xx response to 500. Cover any
// non-trivial value type with a test before relying on it here.
func PublicAttr(key string, value any) PublicDetail {
	return PublicDetail{key: key, value: value}
}

// Key returns the public detail key.
func (d PublicDetail) Key() string { return d.key }

// Value returns the public detail value as an untyped any.
func (d PublicDetail) Value() any { return d.value }

// AsSlogAttr converts the public detail to a slog.Attr for structured
// logging. Used by HTTP error-logging middleware (pkg/httputil log4xx /
// log5xx) that fans Details into slog.Record alongside other attributes;
// not part of the wire serialization path.
//
// For string values, AsSlogAttr returns slog.String (KindString) so that
// pkg/redaction.RedactSlogAttr's free-form RedactString scan can mask
// embedded key=value secrets inside the value text. Non-string values
// pass through as slog.Any (KindAny); RedactSlogAttr's default-passthrough
// on KindAny means structured carriers (LogValuer, custom Stringer) are
// the caller's responsibility to keep sensitive content out of.
func (d PublicDetail) AsSlogAttr() slog.Attr {
	if s, ok := d.value.(string); ok {
		return slog.String(d.key, s)
	}
	return slog.Any(d.key, d.value)
}

// MarshalJSON emits the canonical wire shape {"key":..., "value":...} for
// a single PublicDetail entry. Required because the struct's fields are
// unexported and encoding/json would otherwise skip them.
func (d PublicDetail) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Key   string `json:"key"`
		Value any    `json:"value"`
	}{Key: d.key, Value: d.value})
}

// InternalDetail is the sealed server-only diagnostic key/value attribute
// carried by errcode.Error.InternalDetails. Never marshaled to wire; only
// surfaces in server-side slog records (httputil log4xx / log5xx) and in
// Error.Error() string formatting. Construction is exclusive to
// InternalAttr — same sealed-construction Hard pattern as PublicDetail.
type InternalDetail struct {
	key   string
	value any
}

// InternalAttr constructs an InternalDetail with the given key and value.
// Runtime data (fmt.Sprintf output, identifiers, stack summaries) may
// flow through InternalDetail since the entire channel is server-only.
//
// Free-form single-string convention: when a callsite carries a single
// diagnostic message with no semantic key, use "_" as the key
// (InternalAttr("_", "scan error key=abc")). Error.Error() renders a
// single "_"-keyed entry as the bare value ("[CODE] scan error key=abc")
// rather than "[CODE] _=scan error key=abc", preserving the
// pre-#1035 WithInternal(string) output format. When mixing "_" with
// semantically-keyed entries, the "_=value" prefix appears in Error()
// output — prefer all-semantic keys in new code (e.g. InternalAttr("op",
// op), InternalAttr("query", q)) for clean slog records.
func InternalAttr(key string, value any) InternalDetail {
	return InternalDetail{key: key, value: value}
}

// Key returns the internal detail key.
func (d InternalDetail) Key() string { return d.key }

// Value returns the internal detail value.
func (d InternalDetail) Value() any { return d.value }

// AsSlogAttr converts the internal detail to a slog.Attr for structured
// server-side logging. The HTTP error-logging middleware iterates
// InternalDetails and writes each AsSlogAttr() output into the slog
// record verbatim.
//
// For string values, AsSlogAttr returns slog.String (KindString) so that
// pkg/redaction.RedactSlogAttr's free-form RedactString scan can mask
// embedded key=value secrets. Non-string values pass through as slog.Any
// (KindAny); the caller is responsible for not embedding sensitive
// content in custom Stringer / LogValuer carriers since RedactSlogAttr's
// KindAny path is passthrough by design.
func (d InternalDetail) AsSlogAttr() slog.Attr {
	if s, ok := d.value.(string); ok {
		return slog.String(d.key, s)
	}
	return slog.Any(d.key, d.value)
}
