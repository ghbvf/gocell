// Package wrong_callee_red proves that a _NotFound test calling a similarly
// named non-funnel helper (e.g. testify.assert.Equal) is rejected even when
// it references the right errcode constant.
// fn at line 15; violation emitted at fn.Pos().
package wrong_callee_red

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ghbvf/gocell/pkg/errcode"
)

func TestFoo_NotFound(t *testing.T) {
	// Inline assert.Equal is exactly the mutation-test hazard
	// POSTGRES-NOTFOUND-TEST-OTHER-ERROR-MIXUP-ARCHTEST-01 forbids: no funnel
	// dispatch, so a refactor that changes the assertion type is not caught.
	assert.Equal(t, errcode.ErrSessionNotFound, errcode.ErrSessionNotFound)
}
