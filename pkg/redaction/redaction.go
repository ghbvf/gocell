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
	"errors"
	"fmt"
	"regexp"
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
	`private[_-]?key|signing[_-]?key|dsn`

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
