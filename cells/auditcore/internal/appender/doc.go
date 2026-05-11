// Package appender is the single-source implementation of the auditcore
// audit-append behavior, shared by the four slice packages
// auditappend{user,config,session,role}.
//
// The slice packages exist to carry slice.yaml metadata (subscription
// contracts, verify lists, consumer-group identifiers); their service.go
// files are thin facades:
//
//	type Service = appender.Service
//	var Spec     = appender.MustNewSpec("auditappendxxx", appender.Actor...)
//
// The cell composition root (cells/auditcore/cell.go) constructs four
// appender.Service instances by iterating over each slice package's Spec
// and calling appender.NewService directly; all wiring options
// (WithEmitter, WithTxManager) live on this package.
//
// AI-rebust defenses:
//
//   - HandleEvent single-source: type alias to a non-local Service forbids
//     methods at the language level (Hard).
//
//   - Spec sealed: unexported fields make external construction impossible
//     outside MustNewSpec, which whitelists the 4 known slice names (Hard).
//
//   - ActorMode sealed: unexported field plus package-level var instances
//     prevent ad-hoc construction (Hard).
//
//   - cells/auditcore/internal/ Go-internal rule blocks cross-cell import
//     (Hard, language-enforced).
//
//   - AUDITCORE-APPENDER-SINGLE-SOURCE-01 archtest catches the abandonment
//     case where a slice replaces the alias with a fresh struct definition
//     (Medium tripwire).
package appender
