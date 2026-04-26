// Package dto provides topic constants as a separate package. Exercises
// cross-package const resolution via SelectorExpr (dto.TopicSessionCreated).
package dto

// TopicSessionCreated is the canonical topic string for session-created events.
const TopicSessionCreated = "session.created.v1"
