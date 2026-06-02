// Package metadata export_test.go exposes unexported functions to the
// external test package (package metadata_test) without creating an import
// cycle. Test files that need these functions must use package metadata_test.
package metadata

// ExportedDeriveEventSubscribers exposes the unexported deriveEventSubscribers
// function for use in package metadata_test test files.
var ExportedDeriveEventSubscribers = deriveEventSubscribers

// ExportedApplyAssemblyDerivations exposes the unexported applyAssemblyDerivations
// function for use in package metadata_test test files.
var ExportedApplyAssemblyDerivations = applyAssemblyDerivations

// ExportedDeriveWebhookEndpoints exposes the unexported deriveWebhookEndpoints
// function for use in package metadata_test test files (added by PR-2 webhook impl).
var ExportedDeriveWebhookEndpoints = deriveWebhookEndpoints

// ExportedValidateProjectionUniqueness exposes the unexported
// validateProjectionUniqueness function for use in package metadata_test test
// files (added by PR-04 projection/onReset metadata layer).
var ExportedValidateProjectionUniqueness = validateProjectionUniqueness
