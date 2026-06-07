//go:build archtest_fixture

package modulepathfunnelfixture

// greenUnrelated is a constant concatenation that does not fold to the platform
// module path. The detector MUST NOT flag it (negative control: proves the
// prefix check is not vacuously flagging every BinaryExpr).
var _ = "foo" + "bar"
