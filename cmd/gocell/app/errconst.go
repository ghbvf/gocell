package app

// errEmitResultsFmt is the wrapping format every printer write failure is
// reported under across check.go + validate.go. Single source of truth keeps
// the CLI exit-status surface stable: CI scripts and tests can grep on the
// "emit results:" prefix without brittle reliance on multiple call sites
// agreeing on wording.
const errEmitResultsFmt = "emit results: %w"
