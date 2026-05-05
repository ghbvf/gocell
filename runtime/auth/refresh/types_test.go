package refresh_test

import (
	"strings"
	"testing"
	"time"

	"github.com/ghbvf/gocell/runtime/auth/refresh"
)

func TestPolicy_Validate(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		policy  refresh.Policy
		wantErr bool
		errMsg  string
	}{
		{
			name: "valid_policy",
			policy: refresh.Policy{
				ReuseInterval:  time.Second,
				MaxAge:         time.Hour,
				MaxIdle:        refresh.DefaultMaxIdle,
				GraceMaxReuses: refresh.DefaultGraceMaxReuses,
			},
			wantErr: false,
		},
		{
			name: "MaxAge_zero_isError",
			policy: refresh.Policy{
				ReuseInterval:  time.Second,
				MaxAge:         0,
				MaxIdle:        refresh.DefaultMaxIdle,
				GraceMaxReuses: refresh.DefaultGraceMaxReuses,
			},
			wantErr: true,
			errMsg:  "MaxAge",
		},
		{
			name: "MaxAge_negative_isError",
			policy: refresh.Policy{
				ReuseInterval:  time.Second,
				MaxAge:         -time.Hour,
				MaxIdle:        refresh.DefaultMaxIdle,
				GraceMaxReuses: refresh.DefaultGraceMaxReuses,
			},
			wantErr: true,
			errMsg:  "MaxAge",
		},
		{
			name: "ReuseInterval_negative_isError",
			policy: refresh.Policy{
				ReuseInterval:  -time.Second,
				MaxAge:         time.Hour,
				MaxIdle:        refresh.DefaultMaxIdle,
				GraceMaxReuses: refresh.DefaultGraceMaxReuses,
			},
			wantErr: true,
			errMsg:  "ReuseInterval",
		},
		{
			name: "MaxIdle_zero_isError",
			policy: refresh.Policy{
				ReuseInterval:  time.Second,
				MaxAge:         time.Hour,
				GraceMaxReuses: refresh.DefaultGraceMaxReuses,
				// MaxIdle intentionally zero
			},
			wantErr: true,
			errMsg:  "must be positive",
		},
		{
			name: "GraceMaxReuses_zero_isError",
			policy: refresh.Policy{
				ReuseInterval: time.Second,
				MaxAge:        time.Hour,
				MaxIdle:       30 * 24 * time.Hour,
				// GraceMaxReuses intentionally zero
			},
			wantErr: true,
			errMsg:  "must be positive",
		},
		{
			name: "ReuseInterval_zero_isValid",
			policy: refresh.Policy{
				ReuseInterval:  0,
				MaxAge:         time.Hour,
				MaxIdle:        refresh.DefaultMaxIdle,
				GraceMaxReuses: refresh.DefaultGraceMaxReuses,
			},
			wantErr: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := tc.policy.Validate()
			if tc.wantErr && err == nil {
				t.Errorf("Policy.Validate() = nil, want error")
				return
			}
			if !tc.wantErr && err != nil {
				t.Errorf("Policy.Validate() = %v, want nil", err)
				return
			}
			if tc.wantErr && tc.errMsg != "" && !strings.Contains(err.Error(), tc.errMsg) {
				t.Errorf("Policy.Validate() error = %q, want it to contain %q", err.Error(), tc.errMsg)
			}
		})
	}
}
