package accesscore

import (
	"os"
	"testing"

	"github.com/ghbvf/gocell/kernel/wrapper"
)

// TestMain installs a NoopTracer before any test runs so the package-level
// kernel/wrapper tracer (panicIfNotSetTracer by default) does not panic when
// these cell-level tests exercise auth.Mount-registered contract routes
// without going through runtime.bootstrap.
func TestMain(m *testing.M) {
	wrapper.SetTracer(wrapper.NoopTracer{})
	os.Exit(m.Run())
}
