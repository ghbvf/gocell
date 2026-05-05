//go:build ignore_security_defaults_archtest_fixtures

// Negative fixture: file contains the EXACT bytes "secutil.ValidateTLSEndpoint("
// but only inside a STRING LITERAL and a COMMENT — there is NO real call.
//
// The legacy strings.Contains scan would FALSE-PASS. AST scan must require
// an actual *ast.CallExpr whose Fun is `secutil.ValidateTLSEndpoint`.

package missingtlsvalidate

// secutil.ValidateTLSEndpoint("addr") — comment marker (NOT a real call).
import (
	"fmt"
)

const _hint = `secutil.ValidateTLSEndpoint("string-literal-only")`

// Note: legacy file does NOT import pkg/secutil, but a comment-text scan
// may still pass given the substring "secutil.ValidateTLSEndpoint(" is
// present elsewhere in the file. AST must catch the missing call site.
func init() {
	fmt.Println(_hint)
}
