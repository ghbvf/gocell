// Package webhook_allow_loopback_violate is a synthetic violation fixture for
// the WEBHOOK-ALLOW-LOOPBACK-PROD-BAN-01 archtest. This is a production (non-
// _test.go) file that references webhook.WithAllowLoopback, which the rule bans
// outside _test.go — the reverse self-test asserts the scan fires here.
//
// DO NOT use this package in production code.
package webhook_allow_loopback_violate

import "github.com/ghbvf/gocell/kernel/webhook"

// buildLeakyPolicy is a production-file callsite of WithAllowLoopback — the very
// thing WEBHOOK-ALLOW-LOOPBACK-PROD-BAN-01 forbids outside _test.go.
func buildLeakyPolicy() *webhook.SafePolicy {
	return webhook.NewSafePolicy(webhook.WithAllowLoopback()) // VIOLATION
}
