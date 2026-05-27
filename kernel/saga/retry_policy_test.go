package saga

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/testutil/testtime"
)

// ---------------------------------------------------------------------------
// RetryPolicy.Validate tests
// ---------------------------------------------------------------------------

func TestRetryPolicy_Validate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		policy   RetryPolicy
		wantErr  bool
		wantKind errcode.Kind
	}{
		{
			name:   "zero value is valid (inherit)",
			policy: RetryPolicy{},
		},
		{
			name:   "single attempt no intervals",
			policy: RetryPolicy{MaxAttempts: 1},
		},
		{
			name:   "full policy",
			policy: RetryPolicy{MaxAttempts: 5, BaseInterval: testtime.D1s, MaxInterval: testtime.D30s},
		},
		{
			name:   "base equals max",
			policy: RetryPolicy{MaxAttempts: 3, BaseInterval: testtime.D1s, MaxInterval: testtime.D1s},
		},
		{
			name:     "negative MaxAttempts",
			policy:   RetryPolicy{MaxAttempts: -1},
			wantErr:  true,
			wantKind: errcode.KindInvalid,
		},
		{
			name:     "negative BaseInterval",
			policy:   RetryPolicy{BaseInterval: -testtime.D1s},
			wantErr:  true,
			wantKind: errcode.KindInvalid,
		},
		{
			name:     "negative MaxInterval",
			policy:   RetryPolicy{MaxInterval: -testtime.D1s},
			wantErr:  true,
			wantKind: errcode.KindInvalid,
		},
		{
			name:     "MaxInterval below BaseInterval when both set",
			policy:   RetryPolicy{MaxAttempts: 3, BaseInterval: testtime.D30s, MaxInterval: testtime.D1s},
			wantErr:  true,
			wantKind: errcode.KindInvalid,
		},
		{
			name:   "base set max zero is OK (max inherits)",
			policy: RetryPolicy{MaxAttempts: 3, BaseInterval: testtime.D1s},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := tc.policy.Validate()
			if !tc.wantErr {
				assert.NoError(t, err)
				return
			}
			requireErrKind(t, err, tc.wantKind)
		})
	}
}
