package archtest

// wrapper_location.go — importable CELL-RAW-INFRA-WRAPPER-LOCATION-01
// rule constants.
//
// Platform symbol paths are anchored to PlatformModulePath so a module rename
// updates exactly one place. The scanner logic (scanWrapperViolationsFromPass,
// canonicalCalledFunc, isWrapperCallerAllowed, etc.) lives in the _test.go
// file because it is not needed by external Cell repos; only the forbidden-path
// constants and the canonical map need to be in a non-test file for
// importability.
//
// This file is NOT registered in StandardCellRules(): the rule reasons about
// composition-root caller identity for the four platform wrapper functions; it
// depends on the full scanner infrastructure (types.Func + typesutil) that is
// suitable for dogfood runs but whose scanner functions currently live in the
// _test.go.

const (
	// wrapPersistenceWrapForCell is the canonical path for kernel/persistence.WrapForCell.
	wrapPersistenceWrapForCell = PlatformFrameworkModulePath + "/kernel/persistence.WrapForCell"

	// wrapOutboxWrapPublisherForCell is the canonical path for kernel/outbox.WrapPublisherForCell.
	wrapOutboxWrapPublisherForCell = PlatformFrameworkModulePath + "/kernel/outbox.WrapPublisherForCell"

	// wrapOutboxWrapWriterForCell is the canonical path for kernel/outbox.WrapWriterForCell.
	wrapOutboxWrapWriterForCell = PlatformFrameworkModulePath + "/kernel/outbox.WrapWriterForCell"

	// wrapOutboxWrapEmitterForCell is the canonical path for kernel/outbox.WrapEmitterForCell.
	wrapOutboxWrapEmitterForCell = PlatformFrameworkModulePath + "/kernel/outbox.WrapEmitterForCell"
)

// wrapperFunctionsCanonical is the closed set of wrapper functions whose
// callers are restricted by CELL-RAW-INFRA-WRAPPER-LOCATION-01. Adding a
// new wrapper requires updating this set AND wrapperLocationAllowlistDoc
// (godoc in wrapper_location_test.go) so the rule's surface stays trivially
// auditable.
var wrapperFunctionsCanonical = map[string]bool{
	wrapPersistenceWrapForCell:     true,
	wrapOutboxWrapPublisherForCell: true,
	wrapOutboxWrapWriterForCell:    true,
	wrapOutboxWrapEmitterForCell:   true,
}
