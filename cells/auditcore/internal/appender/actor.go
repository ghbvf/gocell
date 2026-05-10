package appender

import "encoding/json"

// extractActor pulls the actor identity out of an audit event payload using
// the slice's configured ActorMode strategy.
//
//   - ActorAcceptUserFallback: prefer payload.actorId, fall back to
//     payload.userId; used by user/config/session slices where userId
//     identifies the subject and may double as actor in self-service flows.
//
//   - ActorRequireExplicit: only payload.actorId is accepted; userId in
//     role events identifies the target, not the actor. B2-C-05 fail-closed.
//
// Returns ("", false) when payload cannot be parsed as JSON or when the
// chosen field(s) are absent/empty. Callers must Reject with PermanentError
// on (_, false) to keep the contract consistent across the four slices.
func extractActor(payload []byte, mode ActorMode) (string, bool) {
	var p struct {
		ActorID string `json:"actorId"`
		UserID  string `json:"userId"`
	}
	if err := json.Unmarshal(payload, &p); err != nil {
		return "", false
	}
	if p.ActorID != "" {
		return p.ActorID, true
	}
	if mode == ActorAcceptUserFallback && p.UserID != "" {
		return p.UserID, true
	}
	return "", false
}
