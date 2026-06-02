package idempotencytest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// marshalMinimalResponse produces a JSON blob that [idemhttp.UnmarshalRecordedResponse]
// accepts. It mirrors the internal recordedResponseDTO wire layout without
// importing unexported symbols.
//
// encoding/json marshals []byte as base64-encoded strings, which matches the
// internal recordedResponseDTO wire schema expected by UnmarshalRecordedResponse.
func marshalMinimalResponse(status int, body []byte, hdr http.Header) ([]byte, error) {
	type dto struct {
		Status     int                 `json:"status"`
		Body       []byte              `json:"body"`
		Header     map[string][]string `json:"header"`
		RecordedAt time.Time           `json:"recordedAt"`
	}
	d := dto{
		Status:     status,
		Body:       body,
		Header:     hdr,
		RecordedAt: time.Now(),
	}
	out, err := json.Marshal(d)
	if err != nil {
		return nil, fmt.Errorf("marshalMinimalResponse: %w", err)
	}
	return out, nil
}
