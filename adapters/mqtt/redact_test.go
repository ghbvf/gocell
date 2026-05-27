package mqtt

import (
	"errors"
	"strings"
	"testing"
)

func TestRedactConnectURL_WithCredentials(t *testing.T) {
	tests := []struct {
		name    string
		rawURL  string
		wantOut string // substring that must NOT appear
		desc    string
	}{
		{
			name:    "tcp-with-credentials",
			rawURL:  "tcp://user:pass@host:1883",
			wantOut: "pass",
			desc:    "password must not appear in output",
		},
		{
			name:    "mqtt-with-credentials",
			rawURL:  "mqtt://admin:secretpassword@broker.example.com:1883",
			wantOut: "secretpassword",
			desc:    "password must not appear in output",
		},
		{
			name:    "no-credentials",
			rawURL:  "tcp://host:1883",
			wantOut: "",
			desc:    "no credentials — output should not change meaningfully",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out := redactConnectURL(tc.rawURL)
			if tc.wantOut != "" && strings.Contains(out, tc.wantOut) {
				t.Errorf("redactConnectURL(%q) = %q: %s", tc.rawURL, out, tc.desc)
			}
		})
	}
}

func TestRedactConnectURL_Unparseable(t *testing.T) {
	// Unparseable URL falls back to RedactString; confirm it still doesn't echo raw creds.
	raw := "not a url token=abc123"
	out := redactConnectURL(raw)
	if strings.Contains(out, "abc123") {
		t.Errorf("redactConnectURL(%q) = %q: token value must be redacted", raw, out)
	}
}

func TestRedactPayloadForLog(t *testing.T) {
	payload := []byte(`{"password":"hunter2","user":"alice"}`)
	out := redactPayloadForLog(payload)
	if strings.Contains(string(out), "hunter2") {
		t.Errorf("redactPayloadForLog: password value not redacted, got: %s", out)
	}
	// user should still be visible
	if !strings.Contains(string(out), "alice") {
		t.Errorf("redactPayloadForLog: user field should not be redacted, got: %s", out)
	}
}

func TestRedactErr(t *testing.T) {
	err := errors.New("connect failed: token=abc123 host=broker")
	out := redactErr(err)
	if strings.Contains(out.Error(), "abc123") {
		t.Errorf("redactErr: token value not masked in %q", out.Error())
	}
	// host (non-sensitive) should remain
	if !strings.Contains(out.Error(), "host") {
		t.Errorf("redactErr: non-sensitive key 'host' should remain, got: %q", out.Error())
	}
}

func TestRedactErr_Nil(t *testing.T) {
	if out := redactErr(nil); out != nil {
		t.Errorf("redactErr(nil) = %v, want nil", out)
	}
}
