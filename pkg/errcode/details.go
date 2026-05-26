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
// truth for error.details entries (contracts/shared/errors/error-response-v1.schema.json).
type PublicDetail struct {
	key   string
	value any
}

// PublicAttr constructs a PublicDetail with the given key and value. The
// value must be JSON-marshalable; non-marshalable values surface as
// json.Marshal errors at serialization time, not at construction.
func PublicAttr(key string, value any) PublicDetail {
	return PublicDetail{key: key, value: value}
}

// Key returns the public detail key.
func (d PublicDetail) Key() string { return d.key }

// Value returns the public detail value as an untyped any.
func (d PublicDetail) Value() any { return d.value }

// AsSlogAttr converts the public detail to a slog.Attr for structured
// logging. Used by HTTP error-logging middleware that fans Details into
// slog.Record alongside other attributes; not part of the wire path.
func (d PublicDetail) AsSlogAttr() slog.Attr { return slog.Any(d.key, d.value) }

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
func InternalAttr(key string, value any) InternalDetail {
	return InternalDetail{key: key, value: value}
}

// Key returns the internal detail key.
func (d InternalDetail) Key() string { return d.key }

// Value returns the internal detail value.
func (d InternalDetail) Value() any { return d.value }

// AsSlogAttr converts the internal detail to a slog.Attr for structured
// server-side logging. The entire InternalDetails set is fanned into the
// slog record verbatim by the HTTP error-logging middleware.
func (d InternalDetail) AsSlogAttr() slog.Attr { return slog.Any(d.key, d.value) }
