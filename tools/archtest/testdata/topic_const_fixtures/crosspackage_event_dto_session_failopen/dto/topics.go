// Package dto provides event-prefixed topic constants as a separate package.
// Exercises cross-package const resolution via SelectorExpr.
package dto

// TopicSessionCreated is the canonical project topic string for session-created events.
const TopicSessionCreated = "event.session.created.v1"
