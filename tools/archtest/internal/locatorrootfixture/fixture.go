//go:build archtest_fixture

// Package locatorrootfixture is a RED fixture for LOCATOR-ROOT-CONFINED-01
// (tools/archtest/locator_root_confined_test.go). It holds intentional os.DirFS
// usages — a direct call AND a func-value reference — so the archtest's
// anti-vacuity / reverse self-check can prove its os.DirFS resolver works
// without depending on any production callsite (all production metadata reads
// are root-confined via os.OpenRoot after #1592, so zero os.DirFS calls remain
// in the scanned packages). Gated by the archtest_fixture build tag; never part
// of a production build.
package locatorrootfixture

import (
	"io/fs"
	"os"
)

// DirectCall exercises Pass 1 (the direct os.DirFS(...) call form).
func DirectCall() fs.FS { return os.DirFS(".") }

// FuncValue exercises Pass 2 (os.DirFS referenced as a func value, i.e.
// func-value laundering — a selector that is not a call's Fun).
var FuncValue = os.DirFS
