package idempotency

import (
	"bytes"
	"net/http"
	"testing"
	"time"

	"github.com/ghbvf/gocell/kernel/clock/clockmock"
)

// TestRecordedResponseGetters verifies that getters return the values passed to
// newRecordedResponse and that defensive copies prevent mutation of internal state.
func TestRecordedResponseGetters(t *testing.T) {
	t.Parallel()

	clk := clockmock.New(time.Date(2024, 6, 1, 12, 0, 0, 0, time.UTC))

	originalBody := []byte(`{"hello":"world"}`)
	originalHeader := http.Header{
		"Content-Type": []string{"application/json"},
		"X-Trace-Id":   []string{"abc-123"},
	}

	rec := newRecordedResponse(clk, 200, originalBody, originalHeader)

	if got := rec.Status(); got != 200 {
		t.Errorf("Status() = %d, want 200", got)
	}

	if got := rec.Body(); !bytes.Equal(got, originalBody) {
		t.Errorf("Body() = %q, want %q", got, originalBody)
	}

	h := rec.Header()
	if got := h.Get("Content-Type"); got != "application/json" {
		t.Errorf("Header Content-Type = %q, want %q", got, "application/json")
	}

	if got := rec.RecordedAt(); !got.Equal(clk.Now()) {
		t.Errorf("RecordedAt() = %v, want %v", got, clk.Now())
	}
}

// TestRecordedResponseDefensiveCopy verifies that mutating the returned Body or
// Header slice/map does not affect subsequent calls (defensive copy).
func TestRecordedResponseDefensiveCopy(t *testing.T) {
	t.Parallel()

	clk := clockmock.New(time.Time{})

	body := []byte(`{"id":"1"}`)
	hdr := http.Header{"X-Req-Id": []string{"orig"}}
	rec := newRecordedResponse(clk, 201, body, hdr)

	// Mutate the returned body.
	b1 := rec.Body()
	b1[0] = 'Z'
	b2 := rec.Body()
	if b2[0] == 'Z' {
		t.Error("Body() returned a shared slice — mutations affect internal state")
	}

	// Mutate the returned header.
	h1 := rec.Header()
	h1.Set("X-Req-Id", "mutated")
	h2 := rec.Header()
	if h2.Get("X-Req-Id") == "mutated" {
		t.Error("Header() returned a shared map — mutations affect internal state")
	}

	// Also verify that mutating the original inputs at construction time doesn't
	// affect the stored copy.
	body2 := []byte(`{"id":"2"}`)
	hdr2 := http.Header{"X-Foo": []string{"bar"}}
	rec2 := newRecordedResponse(clk, 200, body2, hdr2)
	body2[0] = 'X'
	hdr2.Set("X-Foo", "baz")
	if got := rec2.Body(); got[0] == 'X' {
		t.Error("newRecordedResponse did not clone body — constructor input mutation affects stored copy")
	}
	if rec2.Header().Get("X-Foo") == "baz" {
		t.Error("newRecordedResponse did not clone header — constructor input mutation affects stored copy")
	}
}

// TestMarshalUnmarshalRoundTrip verifies that Marshal→Unmarshal preserves all
// fields including nanosecond-precision recordedAt.
func TestMarshalUnmarshalRoundTrip(t *testing.T) {
	t.Parallel()

	clk := clockmock.New(time.Date(2024, 6, 2, 9, 30, 15, 123456789, time.UTC))

	orig := newRecordedResponse(clk, 200,
		[]byte(`{"data":"ok"}`),
		http.Header{
			"Content-Type": []string{"application/json"},
			"X-Custom":     []string{"value1", "value2"},
		},
	)

	raw, err := MarshalRecordedResponse(orig)
	if err != nil {
		t.Fatalf("MarshalRecordedResponse() error: %v", err)
	}

	got, err := UnmarshalRecordedResponse(raw)
	if err != nil {
		t.Fatalf("UnmarshalRecordedResponse() error: %v", err)
	}

	if got.Status() != orig.Status() {
		t.Errorf("Status: got %d, want %d", got.Status(), orig.Status())
	}
	if !bytes.Equal(got.Body(), orig.Body()) {
		t.Errorf("Body: got %q, want %q", got.Body(), orig.Body())
	}
	if !got.RecordedAt().Equal(orig.RecordedAt()) {
		t.Errorf("RecordedAt: got %v, want %v", got.RecordedAt(), orig.RecordedAt())
	}
	if got.Header().Get("Content-Type") != orig.Header().Get("Content-Type") {
		t.Errorf("Header Content-Type: got %q, want %q",
			got.Header().Get("Content-Type"), orig.Header().Get("Content-Type"))
	}
	if got.Header().Get("X-Custom") != orig.Header().Get("X-Custom") {
		t.Errorf("Header X-Custom: got %q, want %q",
			got.Header().Get("X-Custom"), orig.Header().Get("X-Custom"))
	}
}

// TestUnmarshalRecordedResponseRejectsInvalidStatus verifies that
// UnmarshalRecordedResponse returns an error for out-of-range HTTP status codes.
func TestUnmarshalRecordedResponseRejectsInvalidStatus(t *testing.T) {
	t.Parallel()

	clk := clockmock.New(time.Time{})
	base := newRecordedResponse(clk, 200, []byte(`{}`), nil)
	raw, _ := MarshalRecordedResponse(base)

	cases := []struct {
		name   string
		status int
	}{
		{"zero", 0},
		{"below_range", 99},
		{"above_range", 600},
		{"negative", -1},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			// Build an invalid raw payload directly with the given status.
			invalidJSON := buildInvalidStatusJSON(tc.status, base.RecordedAt(), raw)
			_, err := UnmarshalRecordedResponse(invalidJSON)
			if err == nil {
				t.Errorf("UnmarshalRecordedResponse accepted invalid status %d — want error", tc.status)
			}
		})
	}
}

// TestUnmarshalRecordedResponseRejectsZeroRecordedAt verifies that
// UnmarshalRecordedResponse returns an error when recordedAt is zero.
func TestUnmarshalRecordedResponseRejectsZeroRecordedAt(t *testing.T) {
	t.Parallel()

	raw := []byte(`{"status":200,"body":"e30=","header":{},"recordedAt":"0001-01-01T00:00:00Z"}`)
	_, err := UnmarshalRecordedResponse(raw)
	if err == nil {
		t.Error("UnmarshalRecordedResponse accepted zero recordedAt — want error")
	}
}

// buildInvalidStatusJSON is a test helper that constructs a JSON payload with
// the given status code, using the recorded-at time from an existing valid record.
func buildInvalidStatusJSON(status int, recordedAt time.Time, _ []byte) []byte {
	ts := recordedAt.UTC().Format(time.RFC3339Nano)
	return []byte(`{"status":` + itoa(status) + `,"body":"e30=","header":{},"recordedAt":"` + ts + `"}`)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	pos := len(buf)
	for n > 0 {
		pos--
		buf[pos] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}
