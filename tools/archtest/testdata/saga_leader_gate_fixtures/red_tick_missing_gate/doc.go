//go:build archtest_fixture

// Package redtickmissinggate is a RED fixture for SAGA-DRIVE-BEHIND-LEADER-GATE-01
// A2: tickOnce calls driveOne but never calls acquireLead, so the gate does not
// guard the drive. Expect one A2 diagnostic at the tickOnce declaration.
package redtickmissinggate
