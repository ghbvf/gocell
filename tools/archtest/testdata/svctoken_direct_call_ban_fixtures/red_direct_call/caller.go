// Package reddirectcall is a RED fixture for SVCTOKEN-CALLER-CELL-REQUIRED-01
// C-arm: a non-auth production package calling auth.GenerateServiceToken
// directly, bypassing the auth.SignInternalRequest funnel. Even though the
// callerCell literal ("accesscore") is valid, the C-arm must flag this call
// because it originates outside runtime/auth in a non-test file.
package reddirectcall

import (
	"time"

	"github.com/ghbvf/gocell/framework/pkg/tenant"
	"github.com/ghbvf/gocell/framework/runtime/auth"
)

// directCallerOutsideAuth calls auth.GenerateServiceToken directly from a
// non-auth package — VIOLATION of SVCTOKEN-CALLER-CELL-REQUIRED-01 C-arm.
func directCallerOutsideAuth() string {
	return auth.GenerateServiceToken(nil, "accesscore", "GET", "/internal/v1/example", "", tenant.TenantID(""), "", time.Time{})
}
