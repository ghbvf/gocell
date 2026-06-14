// Package redaction provides fail-closed scrubbing of sensitive substrings in
// error messages and free-form strings before they reach observability backends
// (OTel span attributes, audit logs, last_error storage columns).
//
// 哲学：默认硬编 fail-closed，无调用方 opt-out wiring。dev 调试需要原始 error
// 文本时，从 slog 结构化字段获取（错误日志规范要求结构化关联字段），不靠 trace
// backend。与 hashicorp/vault audit log_raw=false 默认 / golang/go net/url
// URL.Redacted() 哲学一致；OTel SDK / Kratos / Watermill 默认 identity 直通是
// 反例，cell-native 底座不能假设下游一定有 OTel collector redactionprocessor。
//
// ref: hashicorp/vault audit/entry_formatter.go (log_raw=false default)
// ref: golang/go src/net/url/url.go URL.Redacted()
package redaction

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"unicode/utf8"
)

// Mask is the literal substituted in place of redacted values.
const Mask = "<REDACTED>"

// Value-boundary policy across free-form patterns: consume up to the next
// whitespace (or newline for authorization). NEVER stop at `,` or `;` even
// though doing so would preserve more context — fail-closed cannot assume
// secrets are free of those characters. A base64url JWT plus padding, an
// ODBC `Pwd=a;b;c` block embedded inside a larger DSN, or a comma-separated
// secret value would otherwise leak the suffix.

// authorizationPattern handles HTTP `Authorization` header form where the
// value is `Bearer <token>` / `Basic <b64>` (whitespace-separated). value
// extends to end of line so a trailing `;`-suffixed token (rare but not
// impossible inside an opaque bearer) is not leaked. Order matters:
// authorization is applied before defaultPattern so the latter does not
// partially eat `Authorization: Bearer` and leave the real token bare.
var authorizationPattern = regexp.MustCompile(
	`(?i)(authorization)\s*[:=]\s*[^\r\n]+`,
)

// connectionStringPattern handles ODBC / SQL Server connection strings where
// `;` separates fields (e.g. `Server=foo;Pwd=bar;Database=baz`). value
// extends to whitespace so the entire embedded credential block is
// scrubbed at once even when the string itself contains `,` (e.g. SQL
// Server `Server=host,1433;Pwd=secret`).
var connectionStringPattern = regexp.MustCompile(
	`(?i)(connection[_ ]?string)\s*[:=]\s*\S+`,
)

// sensitiveKeyPattern is the single source for key names masked by both
// defaultPattern and quotedJSONPattern. Keep specific token aliases before the
// generic `token` entry so JSON and key=value coverage evolves together.
const sensitiveKeyPattern = `password|passwd|pwd|secret|` +
	`access[_-]?token|refresh[_-]?token|id[_-]?token|token|` +
	`authorization|connection[_ ]?string|api[_-]?key|bearer|` +
	`private[_-]?key|signing[_-]?key|dsn|` +
	// Session identifier — a live-session credential-adjacent token. Matches
	// snake/dash/camel (session_id / session-id / sessionId) via optional
	// separator + (?i). Surfaces in outbox Principal span attrs
	// (gocell.principal.session_id) and audit event payloads; excluded from
	// the auditquery DTO. INV-SESSION-REDACTED (issue #1229).
	`session[_-]?id|` +
	// Webhook signing surface (KERNEL-WEBHOOK-01): inbound/outbound HMAC
	// secrets + signature headers in Svix/Stripe/GitHub naming variants.
	`webhook[_-]?secret|stripe[_-]?signature|x[_-]?signature|` +
	`x[_-]?hub[_-]?signature(?:[_-](?:256|1))?|` +
	`x[_-]?webhook[_-]?signature|svix[_-]?signature|hmac[_-]?key`

// defaultPattern matches single-token `key=value` / `key: value` forms.
//
// 字段集 = outbox 历史集 (password/passwd/secret/token/dsn) + OAuth/OIDC token
// aliases (accessToken/refreshToken/id_token) + wrapper attack surface
// (authorization / connection string / api[_-]?key / bearer / private[_-]?key
// / signing[_-]?key) + pwd (SQL Server / ODBC 连接字符串 password 缩写).
//
// dsn 保留：PG 连接错误 message 常含完整 DSN（含明文密码 postgres://u:pwd@host），
// 是真实泄漏面。
//
// pwd false-positive 已知：日志里出现 `pwd=/home/user` 形式（如打印工作目录）
// 会被 mask。这是 fail-closed 的代价 — 取舍偏向「宁可掩盖目录也不泄漏 SQL
// Server `Pwd=secret`」。dev 调试需要原始 working directory 时走 slog 结构化字段
// (e.g. `slog.String("workdir", v)`)，不靠 trace span。
var defaultPattern = regexp.MustCompile(`(?i)(` + sensitiveKeyPattern + `)\s*[:=]\s*\S+`)

// quotedJSONPattern handles common JSON-in-error fragments such as
// `{"password":"..."}`. It is separate from defaultPattern because the quote
// between the key and `:` is valid JSON but not a key=value separator.
var quotedJSONPattern = regexp.MustCompile(
	`(?i)("(?:` + sensitiveKeyPattern + `)"\s*:\s*)"(?:\\.|[^"\\])*"`,
)

// sensitiveKeyExactPattern matches a bare key name (no surrounding text) that
// names a sensitive field per sensitiveKeyPattern. Single source for any
// "is this key sensitive" check on structured (key, value) pairs where the
// value is bare and free-form regex (defaultPattern / quotedJSONPattern)
// cannot fire — OTel span attributes, errcode.Details slog.Attr, future
// structured scrubbers. Compiled once at package init.
var sensitiveKeyExactPattern = regexp.MustCompile(`(?i)^(` + sensitiveKeyPattern + `)$`)

// IsSensitiveKey reports whether key (case-insensitive) names a sensitive
// field per the package's single-source sensitiveKeyPattern. Used by
// structured-attribute scrubbers where the value is bare and the free-form
// `key=value` regex never fires (e.g., wrapper.Attr{Key: "password",
// Value: "hunter2"} — the value field carries no `password=` anchor token
// so RedactString returns it unchanged).
//
// Dotted namespace keys (OTel span attributes such as
// "gocell.principal.session_id", slog group keys) are matched per dot-delimited
// segment: a sensitive segment anywhere in the key makes the whole key
// sensitive. This is fail-closed — a namespaced attribute whose leaf names a
// credential is masked even though the full dotted string is not itself a bare
// sensitive key. Bare (non-dotted) keys keep the exact-match semantics.
//
// ref: adapters/otel/span.go safeStringAttr; pkg/redaction.RedactSlogAttr.
func IsSensitiveKey(key string) bool {
	if sensitiveKeyExactPattern.MatchString(key) {
		return true
	}
	if strings.ContainsRune(key, '.') {
		for _, seg := range strings.Split(key, ".") {
			if sensitiveKeyExactPattern.MatchString(seg) {
				return true
			}
		}
	}
	return false
}

// allPatterns runs in order. ORDER IS A CORRECTNESS CONSTRAINT, not a
// performance optimization: `authorizationPattern` must consume the full
// `Authorization: Bearer <token>` line before `defaultPattern` runs,
// otherwise `defaultPattern` would match `bearer=` and stop after the
// `Bearer` literal — leaving the real token bare. Likewise
// `connectionStringPattern` consumes the entire ODBC `Server=...;Pwd=...`
// block before `defaultPattern` walks the residue.
type redactionPattern struct {
	re   *regexp.Regexp
	mask func(string) string
}

var allPatterns = []redactionPattern{
	{authorizationPattern, maskAfterSeparator},
	{connectionStringPattern, maskAfterSeparator},
	{quotedJSONPattern, maskJSONStringValue},
	{defaultPattern, maskAfterSeparator},
}

// RedactString masks sensitive `key=value` / `key: value` substrings in s,
// preserving the original key (case + separator) and replacing only the
// value portion with Mask.
func RedactString(s string) string {
	for _, p := range allPatterns {
		s = p.re.ReplaceAllStringFunc(s, p.mask)
	}
	return s
}

// maskAfterSeparator returns the prefix up to and including the `=` or `:`
// separator (and any trailing whitespace) followed by Mask.
//
// Byte-level traversal is safe: every key in the patterns above is ASCII,
// and the separators (`=` 0x3D, `:` 0x3A, ` ` 0x20, `\t` 0x09) are all
// single-byte ASCII characters that cannot appear as UTF-8 continuation
// bytes (0x80–0xBF). If the keyword set is ever extended to include
// non-ASCII letters, switch to a rune-aware scan.
func maskAfterSeparator(match string) string {
	for i := 0; i < len(match); i++ {
		r := match[i]
		if r == '=' || r == ':' {
			j := i + 1
			for j < len(match) && (match[j] == ' ' || match[j] == '\t') {
				j++
			}
			return match[:j] + Mask
		}
	}
	// Unreachable: every pattern requires a `:` or `=` between key and value.
	return match
}

func maskJSONStringValue(match string) string {
	for i := 0; i < len(match); i++ {
		if match[i] != ':' {
			continue
		}
		j := i + 1
		for j < len(match) {
			switch match[j] {
			case ' ', '\t', '\r', '\n':
				j++
			default:
				return match[:j] + `"` + Mask + `"`
			}
		}
	}
	return match
}

// TruncateString truncates s to maxLen runes, preserving valid UTF-8.
// A non-positive maxLen is a no-op (return input as-is).
func TruncateString(s string, maxLen int) string {
	if maxLen <= 0 || utf8.RuneCountInString(s) <= maxLen {
		return s
	}
	runes := []rune(s)
	return string(runes[:maxLen])
}

// RedactError returns an error whose Error() text has sensitive substrings
// masked. nil → nil. When no substitution occurs the original err is returned
// unchanged so errors.Is/As chains are preserved. When substitution occurs
// the returned error is a fresh errors.New — the chain breaks; that is the
// intentional trade-off for telemetry safety.
func RedactError(err error) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	red := RedactString(msg)
	if red == msg {
		return err
	}
	return errors.New(red)
}

// RedactPanic returns the panic value formatted as a string with the same
// secret-masking applied as RedactError / RedactString. It is the canonical
// helper for `slog` panic logging at recover points (HTTP middleware,
// kernel/wrapper, /readyz probe wrappers): a raw panic payload may carry
// arbitrary Go values whose default rendering can include credentials,
// connection strings, or other secrets, so passing it through slog.Any
// without sanitization leaks them into log backends.
//
// Behavior:
//   - error          → RedactError(v).Error() (preserves wrapping if no
//     substitution; fresh string otherwise)
//   - string         → RedactString(v)
//   - other          → fmt.Sprintf("%v", v) then RedactString
//   - nil            → "<nil>" (defensive: panic(nil) is unusual but legal)
//
// ref: hashicorp/vault audit log_raw=false default; ADR
// docs/architecture/202604242030-adr-kernel-wrapper-contract-observability.md §8.
func RedactPanic(v any) string {
	if v == nil {
		return "<nil>"
	}
	switch x := RedactAny(v).(type) {
	case error:
		return x.Error()
	case string:
		return x
	default:
		return fmt.Sprint(x)
	}
}

// RedactSlogAttr returns a copy of attr whose value is redacted. Two-layer
// scrubber:
//
//  1. Key-aware: if attr.Key names a sensitive field (per IsSensitiveKey),
//     the value is replaced with Mask regardless of slog.Value kind. This
//     covers the structured leak where a caller writes
//     errcode.WithDetails(errcode.PublicString("password", userInput)) — the value
//     is a bare string, so the free-form regex in RedactString never
//     matches, and prior to this branch the password was forwarded to slog
//     verbatim.
//  2. Value-aware (non-sensitive key): walk the underlying slog.Value and
//     scrub string contents — String runs through RedactString; Group
//     recurses; other kinds pass through (regex only matches text shapes).
//
// Generated handlers and pkg/httputil log paths use this to scrub user-supplied
// errcode.Error.Details before they reach slog. It complements RedactError
// (which masks string error text); Details are slog.Attr structures, so they
// need the slog.Attr-shaped helper.
//
// # Known limitations
//
// KindAny 边界（Option A，#1036 review F2）：error 与 fmt.Stringer 值是"字符串
// 语义"（后端渲染成一行字符串），故字符串化后走 RedactString——覆盖
// slog.Any("error", err) 与 panic 值（GoCell panic = errcode.Assertion，是
// error）。其余 KindAny（slice / map / 基础类型 / 无 Stringer 的普通 struct）
// passthrough，交给 inner handler 结构化序列化，避免把数组/map 退化成字符串。
// 残余：无 Stringer 且含敏感字段的裸 struct 经 JSON 序列化仍可能泄漏（与
// KindLogValuer 同类已知边界），由 key-aware（敏感 attr key 整体 mask）兜底；
// 彻底的 reflect-walk 深度递归脱敏见 backlog（对标 Vault hashstructure）。
//
// KindLogValuer 刻意走 passthrough（不 Resolve）。唯一的生产 LogValuer 是 readyz
// SlogDependencyEntry，其 error_msg 在构造期已经过 newRedactedErrorMsg→RedactString
// 自脱敏（HEALTH-REDACTED-ERROR-MSG-FUNNEL-01），再 Resolve 重脱敏零安全收益；而
// Resolve 会把 typed value 拍平成 KindGroup，破坏 ops-diagnostics 侧
// `slog.Any(name, SlogDependencyEntry)` → type-assert 回 SlogDependencyEntry 的读取
// 链路（runtime/http/health/healthtest）。业务若未来注入携密的自定义 LogValuer，由
// contract-fanout 纪律 + 此处 known-gap 兜底（与 KindAny 的 fail-closed 取舍不同：
// LogValuer 是显式实现的类型，作者可控，非 recover() 的不可控 any）。
func RedactSlogAttr(attr slog.Attr) slog.Attr {
	if IsSensitiveKey(attr.Key) {
		return slog.Attr{Key: attr.Key, Value: slog.StringValue(Mask)}
	}
	return slog.Attr{Key: attr.Key, Value: redactSlogValue(attr.Value)}
}

func redactSlogValue(v slog.Value) slog.Value {
	switch v.Kind() {
	case slog.KindString:
		return slog.StringValue(RedactString(v.String()))
	case slog.KindGroup:
		attrs := v.Group()
		out := make([]slog.Attr, len(attrs))
		for i, a := range attrs {
			out[i] = RedactSlogAttr(a)
		}
		return slog.GroupValue(out...)
	case slog.KindAny:
		// Option A (#1036 review F2): only string-semantic values (error /
		// fmt.Stringer — this is the panic / error path) are stringified and
		// scrubbed; genuinely structured values pass through so the inner
		// handler serializes them structurally (no array/map → string
		// degradation). See RedactSlogAttr "Known limitations".
		switch v.Any().(type) {
		case error, fmt.Stringer:
			return slog.StringValue(RedactString(fmt.Sprint(v.Any())))
		}
		return v
	default:
		// KindLogValuer and any future kinds pass through unchanged. See the
		// RedactSlogAttr godoc "Known limitations" for the KindLogValuer rationale.
		return v
	}
}

// jsonMaskString is the JSON-encoded fail-closed mask value. It is a valid JSON
// string token (`"<REDACTED>"`), safe to embed as json.RawMessage in a response
// struct. Using a var (not a const) so callers receive a stable byte slice that
// survives multiple calls without reallocation.
var jsonMaskString = []byte(`"` + Mask + `"`)

// RedactPayload scrubs sensitive JSON field values from a raw JSON payload
// before it is returned to API consumers (B2-C-09). It unmarshals the JSON,
// recursively replaces the value of any key whose name matches sensitiveKeyPattern
// with Mask, and re-marshals the result.
//
// Recursive traversal (F-CR-3): nested objects and arrays are traversed so that
// sensitive keys at any depth are scrubbed. Previously only top-level keys were
// visited; deeply nested credentials (e.g., {"user":{"password":"..."}}) were
// silently forwarded to API consumers.
//
// Fail-closed semantics:
//   - Malformed JSON (not parseable at all) → returns jsonMaskString
//     (`"<REDACTED>"`), a valid JSON string token. The old behavior returned
//     []byte(Mask) = []byte("<REDACTED>"), which is NOT valid JSON and causes
//     json.RawMessage embedding to produce a 500.
//   - Marshal failure after traverse (extremely unlikely) → returns jsonMaskString.
//   - Scalar values (numbers, booleans, null) have no keys; redactValue returns
//     them unchanged. They are valid JSON and contain no sensitive key structure.
//   - Top-level JSON arrays are now accepted and recursively traversed.
//
// Note: json.Marshal HTML-escapes the Mask value ("<REDACTED>") to
// "<REDACTED>" in the returned bytes. Callers that embed the
// result as json.RawMessage in a response struct should be aware that a
// default json.Encoder on the outer struct will re-apply the same escaping,
// preserving the unicode-escape form in the wire body.
//
// ref: hashicorp/vault audit/entry_formatter.go — recursive field scrubbing.
func RedactPayload(payload []byte) []byte {
	if len(payload) == 0 {
		return payload
	}
	var v any
	if err := json.Unmarshal(payload, &v); err != nil {
		// Malformed JSON — fail-closed with a valid JSON string token.
		return jsonMaskString
	}
	v = redactValue(v)
	out, err := json.Marshal(v)
	if err != nil {
		// Marshal failure is extremely unlikely; fail-closed.
		return jsonMaskString
	}
	return out
}

// redactValue recursively traverses a JSON-decoded value and replaces the value
// of any map key matching sensitiveKeyExactPattern with Mask. Arrays are
// traversed element-by-element. Scalar values (string, number, bool, nil) are
// returned unchanged — they carry no key structure that could indicate sensitive
// data.
func redactValue(v any) any {
	switch val := v.(type) {
	case map[string]any:
		for k := range val {
			if sensitiveKeyExactPattern.MatchString(k) {
				val[k] = Mask
			} else {
				val[k] = redactValue(val[k])
			}
		}
		return val
	case []any:
		for i := range val {
			val[i] = redactValue(val[i])
		}
		return val
	default:
		// string / float64 / bool / nil — no sensitive key structure; return as-is.
		return v
	}
}

// IPHash is a sealed, keyed, non-reversible hash of a client IP address. It is
// the single sanctioned carrier for client-IP values that cross a replayable
// boundary — outbox rows, broker messages, the DLX, and the audit ledger
// payload — as well as the slog client_ip_hash field.
//
// The unexported field makes a populated IPHash impossible to construct outside
// this package: a plaintext IP string cannot be assigned where an IPHash is
// required (compile error), so an event-payload producer physically cannot emit
// a raw client IP. The only constructor is [HashIP]. The zero value (IsEmpty)
// marshals to "" and signals that no IP was available. There is deliberately no
// UnmarshalJSON: consumers read the already-hashed value as a plain string in
// their own DTOs, keeping the only IPHash-producing path the keyed HashIP — a
// plaintext value cannot be laundered into an IPHash via json.Unmarshal.
//
// Why keyed (HMAC) rather than a bare hash: the IPv4 space is only 2^32, so an
// unsalted SHA-256 of an IP is reversible by brute force (precompute every IP).
// A per-deployment secret salt makes the hash non-reversible while remaining
// deterministic, so "same source IP" correlation across entries is preserved
// without retaining plaintext PII. Mirrors HashiCorp Vault's audit device
// default (HMAC-SHA256 of sensitive fields unless log_raw).
//
// Field set + constructor set + the producer DTO field type are frozen by the
// CLIENT-IP-HASH-FUNNEL-01 archtest. See ADR
// docs/architecture/...-1488-adr-replayable-payload-pii-hash-funnel.md.
type IPHash struct {
	v string
}

// MinIPHashSaltBytes is the minimum salt length composition roots must enforce
// before calling [HashIP] in production. 32 bytes matches the other GoCell HMAC
// secrets (auth.MinHMACKeyBytes); a shorter salt weakens the keyed-hash secrecy.
const MinIPHashSaltBytes = 32

// HashIP returns a keyed HMAC-SHA256 hash of ip, hex-encoded, as a sealed
// [IPHash]. An empty ip returns the zero IPHash (IsEmpty, marshals to "") —
// HMAC of the empty string is non-empty, so the empty case is special-cased to
// preserve the "no IP available" signal end to end.
//
// salt is a per-deployment secret loaded from the environment (see
// cellmodules/cellsecrets). HashIP itself does not validate the salt (it cannot
// return an error). An empty/short salt still produces a valid-looking digest but
// offers NO secrecy — with a known or empty salt the whole IPv4 space (2^32) is
// brute-forceable, so the hash is effectively reversible. Composition roots MUST
// inject a real secret of at least [MinIPHashSaltBytes] bytes in production
// (enforced at the wiring site, e.g. cellmodules/accesscore).
func HashIP(salt []byte, ip string) IPHash {
	if ip == "" {
		return IPHash{}
	}
	mac := hmac.New(sha256.New, salt)
	mac.Write([]byte(ip))
	return IPHash{v: hex.EncodeToString(mac.Sum(nil))}
}

// String returns the hex digest, or "" for the zero value. Use it for the slog
// client_ip_hash field.
func (h IPHash) String() string { return h.v }

// IsEmpty reports whether no IP was hashed (the zero value).
func (h IPHash) IsEmpty() bool { return h.v == "" }

// MarshalJSON encodes the hash as a JSON string ("" for the zero value), so an
// IPHash is wire-compatible with the schema's {"type": "string"} field.
func (h IPHash) MarshalJSON() ([]byte, error) { return json.Marshal(h.v) }

// IsIPHashString reports whether s is a valid wire form of an IPHash digest:
// either empty (no IP was available) or a 64-character lowercase-hex
// HMAC-SHA256 digest. It is the runtime counterpart of the clientIpHash
// JSON-schema pattern "^$|^[a-f0-9]{64}$" and the single source of the IP-hash
// wire shape.
//
// The sealed IPHash type guards the PRODUCER (a plaintext string cannot be
// assigned where an IPHash is required), but a consumer reading the hash from an
// UNTRUSTED wire event (broker / replay / DLX) decodes into a plain string and
// has no such protection — a malformed or plaintext-laundered value could
// otherwise reach the audit ledger. Consumers call IsIPHashString to fail closed
// at that trust boundary (#1488).
func IsIPHashString(s string) bool {
	if s == "" {
		return true
	}
	if len(s) != 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// RedactAny scrubs sensitive substrings from arbitrary panic-style payloads
// before they reach observability backends. Three branches:
//
//   - nil → nil
//   - error → RedactError(e)
//   - string → RedactString(s)
//   - other → RedactString(fmt.Sprint(v)) (covers fmt.Stringer, structs,
//     primitive types; fail-closed: stringify and mask, never echo raw).
//
// Intended for `slog.Any("panic", redaction.RedactAny(r))` in panic recovery
// blocks where r is `any` returned from recover(). The same regex pipeline as
// RedactString applies, so the field-set and over-mask trade-offs documented
// at package level apply uniformly.
//
// Call-site defense-in-depth (no longer archtest-enforced; PANIC-REDACT-01
// retired). Sink-side redaction is covered unconditionally by
// SLOG-HANDLER-SEALED-FUNNEL-01 via RedactSlogAttr's KindAny branch.
func RedactAny(v any) any {
	if v == nil {
		return nil
	}
	switch x := v.(type) {
	case error:
		return RedactError(x)
	case string:
		return RedactString(x)
	default:
		return RedactString(fmt.Sprint(x))
	}
}
