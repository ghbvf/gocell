//go:build archtest_fixture

// Package reddriveoutsidetick is a RED fixture for SAGA-DRIVE-BEHIND-LEADER-GATE-01
// A1: a method other than tickOnce (rogue) calls driveOne directly, bypassing
// the leader-elect gate. Expect one A1 diagnostic at the rogue callsite.
package reddriveoutsidetick
