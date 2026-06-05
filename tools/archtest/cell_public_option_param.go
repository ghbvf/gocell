package archtest

// cell_public_option_param.go — importable CELL-RAW-INFRA-PUBLIC-OPTION-PARAM-01
// rule constants.
//
// Platform symbol paths are anchored to PlatformModulePath so a module rename
// updates exactly one place. The scanner logic (isCellSubtreeFile,
// scanPassForRawPublicOption, publicOptionParamCanonical, etc.) lives in the
// _test.go file because it is not needed by external Cell repos; only the
// forbidden-path constants need to be in a non-test file for importability.
//
// This file is NOT registered in StandardCellRules(): the rule reasons about
// how cells consume platform infra types; it depends on the full scanner
// infrastructure (types.Implements + typesutil) that is suitable for dogfood
// runs but whose scanner functions currently live in the _test.go. A full
// Check* function extraction (parallel to CheckPanicRegistered) is tracked at
// #1302 and can land in a later M3 PR.

const (
	// rawPublicOptionForbiddenPersistenceTxRunner is the forbidden canonical
	// path for kernel/persistence.TxRunner.
	rawPublicOptionForbiddenPersistenceTxRunner = PlatformModulePath + "/kernel/persistence.TxRunner"

	// rawPublicOptionForbiddenOutboxPublisher is the forbidden canonical
	// path for kernel/outbox.Publisher.
	rawPublicOptionForbiddenOutboxPublisher = PlatformModulePath + "/kernel/outbox.Publisher"

	// rawPublicOptionForbiddenOutboxWriter is the forbidden canonical
	// path for kernel/outbox.Writer.
	rawPublicOptionForbiddenOutboxWriter = PlatformModulePath + "/kernel/outbox.Writer"

	// rawPublicOptionForbiddenOutboxEmitter is the forbidden canonical
	// path for kernel/outbox.Emitter.
	rawPublicOptionForbiddenOutboxEmitter = PlatformModulePath + "/kernel/outbox.Emitter"
)
