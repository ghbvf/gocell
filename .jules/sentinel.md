## 2026-04-14 - Redacting Credentials in Parse Errors
**Vulnerability:** Third-party libraries like `pgx` and standard libraries like `net/url` often embed raw inputs, including connection strings, into their error messages. This can lead to credential leakage in logs or HTTP responses when DSN parsing fails.
**Learning:** Error redaction requires caution to ensure we don't accidentally leak the underlying error by wrapping it with `%w`. If the string is changed, flattening the error via `fmt.Errorf` is the correct security posture to prevent unwrapping.
**Prevention:** Always inspect error strings returned by parsing functions before surfacing them. Use robust Regex patterns that handle both URI (`postgres://...`) and Key-Value (`password=...`) formats.
