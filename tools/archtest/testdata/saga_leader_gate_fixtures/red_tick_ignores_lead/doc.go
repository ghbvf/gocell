//go:build archtest_fixture

// Package redtickignoreslead is a RED fixture for SAGA-DRIVE-BEHIND-LEADER-GATE-01
// A3: tickOnce calls acquireLead (so A2 passes) but its lead result never gates
// driveOne — lead is captured and even referenced, yet the drive runs
// unconditionally. Expect one A3 diagnostic at the tickOnce declaration.
package redtickignoreslead
