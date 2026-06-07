//go:build archtest_fixture

package modulepathfunnelfixture

import "github.com/ghbvf/gocell/tools/archtest"

// pDerived is a SAME-package const derived from the real PlatformModulePath.
const pDerived = archtest.PlatformModulePath + "/a"

// The blank var folds to a platform CHILD value (github.com/ghbvf/gocell/a/b) via
// a SAME-package derived const (pDerived), not via PlatformModulePath directly.
// It MUST NOT be flagged: the exemption is TRANSITIVE — constOperandSanctioned
// traces pDerived's RHS back to PlatformModulePath. This exercises the exact
// derived-const path the 369 real funnel-scope derivations rely on (e.g.
// mqttPkgPath = PlatformModulePath+"/adapters/mqtt"; then mqttPkgPath+"/internal/topicns").
var _ = pDerived + "/b"
