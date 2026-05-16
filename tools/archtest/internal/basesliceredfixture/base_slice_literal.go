//go:build archtest_fixture

// Package basesliceredfixture contains intentionally-violating forms for the
// BASESLICE-CTOR-FUNNEL-01 archtest detector. Gated by the archtest_fixture
// build tag (must agree with the "archtest_fixture" literal (single source:
// RunTypedFixture helper in tools/archtest/fixture.go)).
//
// This file exercises RED shape 2: a &cell.BaseSlice{} composite literal,
// which sidesteps both the typed funnel and the metadata projection.
package basesliceredfixture

import "github.com/ghbvf/gocell/kernel/cell"

// VIOLATION: &cell.BaseSlice{} — forbidden composite literal.
// Production code must use cell.MustNewBaseSliceFromMeta(slicePkg.SliceMetadata()).
//
//nolint:all // intentional violation for archtest RED fixture
var redBaseSliceLiteral = &cell.BaseSlice{} //nolint:unused
