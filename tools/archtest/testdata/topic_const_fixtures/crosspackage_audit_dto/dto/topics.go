// Package dto provides audit topic constants as a separate package.
// Exercises cross-package const resolution for non-session security topics.
package dto

// TopicAuditEntryAppended is the canonical topic for audit entry events.
const TopicAuditEntryAppended = "audit.entry-appended.v1"
