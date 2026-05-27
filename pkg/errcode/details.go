package errcode

import (
	"encoding/json"
	"log/slog"
	"time"
)

// PublicDetail is the sealed wire-safe key/value attribute carried by
// errcode.Error.Details. Both fields are unexported so outside-package
// struct-literal construction is inexpressible in Go's type system, and
// the value field is typed publicValue (sealed marker interface) so the
// constructor surface enumerates every wire-safe scalar exactly once.
//
// Wire schema is {"key": string, "value": <scalar>}; the concrete scalar
// types (string / int64 / bool / nanosecond int64 for Duration /
// RFC3339Nano string for Time) match contracts/shared/errors/error-response-v1.schema.json
// which constrains value to {"string","number","boolean"}. Adding a new
// scalar kind requires (a) a new publicValue impl and constructor here,
// (b) the wire schema update, and (c) DETAILS-SEALED-FIELD-FROZEN-01
// archtest update.
//
// Known bypass surface: errcode.Error.Details is exported as []PublicDetail.
// External code can overwrite or append the slice with values constructed
// via the typed Public* constructors. The wire-side 5xx Details-strip
// invariant (Error.MarshalJSON → project() → []PublicDetail{}) is the
// defense for leakage regardless of append source; the sealed-construction
// Hard claim is "no outside code can construct a non-zero PublicDetail
// without going through the typed constructors", not "no outside code can
// mutate Error.Details". Callers should still route input through
// WithDetails(...PublicDetail).
type PublicDetail struct {
	key   string
	value publicValue
}

// publicValue is the sealed marker interface implemented exclusively by the
// concrete value wrappers in this file. The unexported method makes
// outside-package implementations a Go compile error, so the wire-unsafe
// types that PublicString(any) used to accept (channels, functions, NaN/Inf
// floats, maps, structs, pointers) cannot reach errcode.Error.Details.
//
// This replaces the pre-PR #1035 MustValidateDetailsKinds runtime kind
// allowlist with a Hard type-system invariant; see ADR
// docs/architecture/202605051730-adr-errcode-message-pii-safety.md §Amendment.
type publicValue interface {
	publicValue() // sealed marker
	marshalJSONValue() ([]byte, error)
	slogValue() slog.Value
	rawAny() any
}

// publicString / publicInt / publicBool / publicDuration / publicTime are
// the concrete wire-safe value wrappers. Adding a new scalar kind requires
// extending DETAILS-SEALED-FIELD-FROZEN-01 archtest.
type (
	publicString   struct{ v string }
	publicInt      struct{ v int64 }
	publicBool     struct{ v bool }
	publicDuration struct{ v time.Duration }
	publicTime     struct{ v time.Time }
)

func (publicString) publicValue()   {}
func (publicInt) publicValue()      {}
func (publicBool) publicValue()     {}
func (publicDuration) publicValue() {}
func (publicTime) publicValue()     {}

func (p publicString) marshalJSONValue() ([]byte, error)   { return json.Marshal(p.v) }
func (p publicInt) marshalJSONValue() ([]byte, error)      { return json.Marshal(p.v) }
func (p publicBool) marshalJSONValue() ([]byte, error)     { return json.Marshal(p.v) }
func (p publicDuration) marshalJSONValue() ([]byte, error) { return json.Marshal(int64(p.v)) }
func (p publicTime) marshalJSONValue() ([]byte, error)     { return json.Marshal(p.v) }

func (p publicString) slogValue() slog.Value   { return slog.StringValue(p.v) }
func (p publicInt) slogValue() slog.Value      { return slog.Int64Value(p.v) }
func (p publicBool) slogValue() slog.Value     { return slog.BoolValue(p.v) }
func (p publicDuration) slogValue() slog.Value { return slog.DurationValue(p.v) }
func (p publicTime) slogValue() slog.Value     { return slog.TimeValue(p.v) }

func (p publicString) rawAny() any   { return p.v }
func (p publicInt) rawAny() any      { return p.v }
func (p publicBool) rawAny() any     { return p.v }
func (p publicDuration) rawAny() any { return p.v }
func (p publicTime) rawAny() any     { return p.v }

// PublicInteger covers every Go signed integer kind so callers do not need
// explicit int → int64 conversions (covers int, len()/cap() results,
// json.SyntaxError.Offset which is int64, etc). Stored as int64 internally.
// Unsigned kinds are intentionally excluded — no GoCell callsite uses
// uint*; callers that genuinely need uint values must cast at the call
// site to surface the truncation choice.
type PublicInteger interface {
	~int | ~int8 | ~int16 | ~int32 | ~int64
}

// PublicString constructs a string-valued PublicDetail. The most common
// kind — every identifier, enum value, validation reason, error label.
func PublicString(key, value string) PublicDetail {
	return PublicDetail{key: key, value: publicString{v: value}}
}

// PublicInt constructs an integer-valued PublicDetail. Accepts any signed
// integer kind via the PublicInteger type constraint.
func PublicInt[T PublicInteger](key string, value T) PublicDetail {
	return PublicDetail{key: key, value: publicInt{v: int64(value)}}
}

// PublicBool constructs a bool-valued PublicDetail.
func PublicBool(key string, value bool) PublicDetail {
	return PublicDetail{key: key, value: publicBool{v: value}}
}

// PublicDuration constructs a Duration-valued PublicDetail. Serializes as
// integer nanoseconds on the wire (matching slog.DurationValue's encoding
// of the channel pre-PR #1035). Operators surfacing duration via the wire
// should format to human-readable string at the call site and use
// PublicString instead — JSON consumers cannot tell nanoseconds from a
// generic count without out-of-band context.
func PublicDuration(key string, value time.Duration) PublicDetail {
	return PublicDetail{key: key, value: publicDuration{v: value}}
}

// PublicTime constructs a time.Time-valued PublicDetail. Serializes as an
// RFC3339Nano-formatted string (encoding/json's default time.Time format).
func PublicTime(key string, value time.Time) PublicDetail {
	return PublicDetail{key: key, value: publicTime{v: value}}
}

// Key returns the public detail key.
func (d PublicDetail) Key() string { return d.key }

// Value returns the underlying scalar as untyped any for read-side helpers
// (FindAttr, ctxcancel.ReasonFromDetails). The dynamic type is always one
// of: string, int64, bool, time.Duration, time.Time, or nil (zero
// PublicDetail). Mutations through the returned value cannot reach the
// PublicDetail because every concrete value wrapper stores by value.
func (d PublicDetail) Value() any {
	if d.value == nil {
		return nil
	}
	return d.value.rawAny()
}

// AsSlogAttr converts the public detail to a slog.Attr for structured
// logging. Used by HTTP error-logging middleware (pkg/httputil log4xx /
// log5xx) that fans Details into slog.Record alongside other attributes;
// not part of the wire serialization path.
//
// The typed value wrappers route each scalar kind to the matching
// slog.Value constructor (StringValue / Int64Value / BoolValue /
// DurationValue / TimeValue). String values surface as slog.KindString so
// pkg/redaction.RedactSlogAttr's free-form RedactString scan can mask
// embedded key=value secrets.
func (d PublicDetail) AsSlogAttr() slog.Attr {
	if d.value == nil {
		return slog.Attr{Key: d.key}
	}
	return slog.Attr{Key: d.key, Value: d.value.slogValue()}
}

// MarshalJSON emits the canonical wire shape {"key":..., "value":...} for
// a single PublicDetail entry. Unexported fields are otherwise skipped by
// encoding/json. Wire-unsafe values (NaN/Inf, channels, etc.) cannot reach
// this point — the typed constructor surface excludes them at compile time.
func (d PublicDetail) MarshalJSON() ([]byte, error) {
	var raw json.RawMessage
	if d.value == nil {
		raw = json.RawMessage("null")
	} else {
		b, err := d.value.marshalJSONValue()
		if err != nil {
			return nil, err
		}
		raw = b
	}
	return json.Marshal(struct {
		Key   string          `json:"key"`
		Value json.RawMessage `json:"value"`
	}{Key: d.key, Value: raw})
}

// InternalDetail is the sealed server-only diagnostic key/value attribute
// carried by errcode.Error.InternalDetails. Never marshaled to wire; only
// surfaces in server-side slog records (httputil log4xx / log5xx) and in
// Error.Error() string formatting. Construction is exclusive to
// InternalAttr — sealed-construction Hard pattern via unexported fields.
//
// Unlike PublicDetail, the value field is typed any: the entire channel is
// server-only, so wire safety does not constrain accepted types and
// runtime data (fmt.Sprintf output, identifiers, stack summaries) may flow
// through. The wire-safety enforcement applies only to PublicDetail per
// ADR docs/architecture/202605051730-adr-errcode-message-pii-safety.md
// §Amendment.
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
