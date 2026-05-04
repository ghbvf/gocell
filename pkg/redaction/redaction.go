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
	"regexp"
	"unicode/utf8"
)

// Mask is the literal substituted in place of redacted values.
const Mask = "<REDACTED>"

// authorizationPattern handles HTTP `Authorization` header form where the
// value is `Bearer <token>` / `Basic <b64>` (whitespace-separated). value
// extends to end of line / `;` so the trailing token is not leaked. Order
// matters: authorization is applied before defaultPattern so the latter does
// not partially eat `Authorization: Bearer` and leave the real token bare.
var authorizationPattern = regexp.MustCompile(
	`(?i)(authorization)\s*[:=]\s*[^\r\n;]+`,
)

// connectionStringPattern handles ODBC / SQL Server connection strings where
// `;` separates fields (e.g. `Server=foo;Pwd=bar;Database=baz`). value
// extends to whitespace / comma so the entire embedded credential block is
// scrubbed at once.
var connectionStringPattern = regexp.MustCompile(
	`(?i)(connection[_ ]?string)\s*[:=]\s*[^\s,]+`,
)

// defaultPattern matches single-token `key=value` / `key: value` forms.
//
// 字段集 = outbox 历史集 (password/passwd/secret/token/dsn) + wrapper attack
// surface (api[_-]?key / bearer / private[_-]?key / signing[_-]?key) + pwd
// (SQL Server / ODBC 连接字符串 password 缩写).
//
// dsn 保留：PG 连接错误 message 常含完整 DSN（含明文密码 postgres://u:pwd@host），
// 是真实泄漏面。dsn 的 value 在 PG 风格里是单 URL（无 `;`），保持单词边界足够。
//
// pwd false-positive 已知：日志里出现 `pwd=/home/user` 形式（如打印工作目录）
// 会被 mask。这是 fail-closed 的代价 — 取舍偏向「宁可掩盖目录也不泄漏 SQL
// Server `Pwd=secret`」。dev 调试需要原始 working directory 时走 slog 结构化字段
// (e.g. `slog.String("workdir", v)`)，不靠 trace span。
var defaultPattern = regexp.MustCompile(
	`(?i)(password|passwd|pwd|secret|token|api[_-]?key|bearer|private[_-]?key|signing[_-]?key|dsn)\s*[:=]\s*[^\s;,]+`,
)

// allPatterns runs in order. ORDER IS A CORRECTNESS CONSTRAINT, not a
// performance optimization: `authorizationPattern` must consume the full
// `Authorization: Bearer <token>` line before `defaultPattern` runs,
// otherwise `defaultPattern` would match `bearer=` and stop after the
// `Bearer` literal — leaving the real token bare. Likewise
// `connectionStringPattern` consumes the entire ODBC `Server=...;Pwd=...`
// block before `defaultPattern` walks the residue.
var allPatterns = []*regexp.Regexp{
	authorizationPattern,
	connectionStringPattern,
	defaultPattern,
}

// RedactString masks sensitive `key=value` / `key: value` substrings in s,
// preserving the original key (case + separator) and replacing only the
// value portion with Mask.
func RedactString(s string) string {
	for _, p := range allPatterns {
		s = p.ReplaceAllStringFunc(s, maskAfterSeparator)
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
