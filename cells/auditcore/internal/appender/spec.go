package appender

import (
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/panicregister"
)

// Spec is the sealed per-slice configuration consumed by NewService. The
// unexported field forces construction through MustNewSpec, which validates
// against the closed set of slice names auditcoreAppenderSliceNames.
type Spec struct {
	name string
}

// Name returns the slice name (used as the log/error prefix).
func (s Spec) Name() string { return s.name }

// auditcoreAppenderSliceNames is the closed set of permitted slice names.
// Adding a new auditappend* slice requires extending both this list and
// the AUDITCORE-APPENDER-SINGLE-SOURCE-01 archtest's package list.
var auditcoreAppenderSliceNames = []string{
	"auditappenduser",
	"auditappendconfig",
	"auditappendsession",
	"auditappendrole",
}

// MustNewSpec constructs a Spec for the named auditappend slice. Panics
// (PANIC-REGISTERED-01 A-class: configuration error at init time) when
// name is not in the auditcoreAppenderSliceNames whitelist.
func MustNewSpec(name string) Spec {
	for _, allowed := range auditcoreAppenderSliceNames {
		if name == allowed {
			return Spec{name: name}
		}
	}
	panic(panicregister.Approved("appender-spec-unknown-name", errcode.Assertion(
		"appender.MustNewSpec: unknown slice name %q; whitelist: %s",
		name, joinNames(auditcoreAppenderSliceNames))))
}

func joinNames(names []string) string {
	out := ""
	for i, n := range names {
		if i > 0 {
			out += ", "
		}
		out += n
	}
	return out
}
