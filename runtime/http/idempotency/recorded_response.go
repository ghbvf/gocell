package idempotency

import (
	"bytes"
	"encoding/json"
	"net/http"
	"time"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/pkg/errcode"
)

// RecordedResponse is the sealed blob stored by the idempotency middleware for
// future replay on ClaimDone. All fields are unexported; consumers read state
// through the value-receiver getters below.
//
// Sealed construction: the only ways to obtain a RecordedResponse are
// newRecordedResponse (package-internal producer constructor used by the HTTP
// middleware after handler execution) and UnmarshalRecordedResponse (the
// wire-decode funnel used by Store implementations when loading a stored blob).
// Composite literals RecordedResponse{...} outside this package are a compile
// error (all fields unexported).
type RecordedResponse struct {
	status     int
	body       []byte
	header     http.Header
	recordedAt time.Time
}

// Status returns the HTTP status code of the recorded response.
func (r RecordedResponse) Status() int { return r.status }

// Body returns a defensive copy of the recorded response body. Mutations to
// the returned slice do not affect the stored copy.
func (r RecordedResponse) Body() []byte { return bytes.Clone(r.body) }

// Header returns a clone of the recorded response headers. Mutations to the
// returned map do not affect the stored copy.
func (r RecordedResponse) Header() http.Header { return r.header.Clone() }

// RecordedAt returns the wall-clock time at which this response was recorded,
// derived from the injected clock at construction time.
func (r RecordedResponse) RecordedAt() time.Time { return r.recordedAt }

// newRecordedResponse is the package-internal constructor. It clones all mutable
// inputs defensively so callers cannot corrupt stored state via aliased slices.
// recordedAt is stamped from clk.Now().
func newRecordedResponse(clk clock.Clock, status int, body []byte, h http.Header) RecordedResponse {
	return RecordedResponse{
		status:     status,
		body:       bytes.Clone(body),
		header:     h.Clone(),
		recordedAt: clk.Now(),
	}
}

// recordedResponseDTO is the internal JSON transfer object used for
// marshal/unmarshal only. The exported field names match the wire schema;
// the sealed RecordedResponse type itself stays unexported-field.
type recordedResponseDTO struct {
	Status     int         `json:"status"`
	Body       []byte      `json:"body"` // encoded as base64 by encoding/json
	Header     http.Header `json:"header"`
	RecordedAt time.Time   `json:"recordedAt"`
}

// MarshalRecordedResponse serializes r to JSON for storage by a Store
// implementation. The inverse is UnmarshalRecordedResponse.
func MarshalRecordedResponse(r RecordedResponse) ([]byte, error) {
	dto := recordedResponseDTO{
		Status:     r.status,
		Body:       r.body,
		Header:     r.header,
		RecordedAt: r.recordedAt,
	}
	return json.Marshal(dto)
}

// UnmarshalRecordedResponse deserializes raw (produced by MarshalRecordedResponse)
// back into a RecordedResponse. It validates that status is in [100, 599] and
// that recordedAt is non-zero; any violation returns an errcode error.
func UnmarshalRecordedResponse(raw []byte) (RecordedResponse, error) {
	var dto recordedResponseDTO
	if err := json.Unmarshal(raw, &dto); err != nil {
		return RecordedResponse{}, errcode.New(
			errcode.KindInvalid,
			errcode.ErrValidationFailed,
			"idempotency: recorded response decode failed",
			errcode.WithInternal(errcode.InternalAttr("_", err.Error())),
		)
	}
	if dto.Status < 100 || dto.Status > 599 {
		return RecordedResponse{}, errcode.New(
			errcode.KindInvalid,
			errcode.ErrValidationFailed,
			"idempotency: recorded response has invalid HTTP status",
			errcode.WithDetails(errcode.PublicInt("status", dto.Status)),
		)
	}
	if dto.RecordedAt.IsZero() {
		return RecordedResponse{}, errcode.New(
			errcode.KindInvalid,
			errcode.ErrValidationFailed,
			"idempotency: recorded response has zero recordedAt",
		)
	}
	return RecordedResponse{
		status:     dto.Status,
		body:       dto.Body,
		header:     dto.Header,
		recordedAt: dto.RecordedAt,
	}, nil
}
