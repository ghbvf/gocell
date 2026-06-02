//go:build archtest_fixture

// Package redsagaslogattrliteral is a RED fixture for
// SAGA-SLOG-INSTANCE-FIELDS-CALLER-01 B4: per-instance identity attrs built as
// raw log/slog.Attr composite literals — keyed (slog.Attr{Key: …}) and unkeyed
// (slog.Attr{…} positional) — bypassing the sagalog.InstanceFields carrier. The
// B4 literal scan must flag both. Expect two diagnostics (#1266 review C1/F2).
package redsagaslogattrliteral
