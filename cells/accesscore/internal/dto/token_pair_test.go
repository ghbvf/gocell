package dto

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestToTokenPairResponse(t *testing.T) {
	expires := time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		in   TokenPair
		want TokenPairResponse
	}{
		{"all fields populated",
			TokenPair{AccessToken: "at", RefreshToken: "rt", ExpiresAt: expires,
				SessionID: "sess-1", UserID: "usr-1", PasswordResetRequired: true},
			TokenPairResponse{AccessToken: "at", RefreshToken: "rt", ExpiresAt: expires,
				SessionID: "sess-1", UserID: "usr-1", PasswordResetRequired: true}},
		{"zero value pair",
			TokenPair{},
			TokenPairResponse{}},
		{"reset flag false defaults preserved",
			TokenPair{AccessToken: "x", RefreshToken: "y", ExpiresAt: expires, SessionID: "s", UserID: "u", PasswordResetRequired: false},
			TokenPairResponse{AccessToken: "x", RefreshToken: "y", ExpiresAt: expires, SessionID: "s", UserID: "u", PasswordResetRequired: false}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, ToTokenPairResponse(tc.in))
		})
	}
}
