package initialadmin

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseDACLAces(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		sddl      string
		wantLen   int
		wantFirst sddlAce
		wantErr   bool
	}{
		{
			name:      "three ACEs with mixed alias and full SID forms",
			sddl:      "D:PAI(A;;FA;;;LA)(A;;FA;;;BA)(A;;FA;;;SY)",
			wantLen:   3,
			wantFirst: sddlAce{aceType: "A", sidStr: "LA"},
		},
		{
			name:      "single ACE with hex rights and full SID",
			sddl:      "D:P(A;;0x1f01ff;;;S-1-5-18)",
			wantLen:   1,
			wantFirst: sddlAce{aceType: "A", sidStr: "S-1-5-18"},
		},
		{
			name:      "DENY then ALLOW preserved in order",
			sddl:      "D:P(D;;FA;;;WD)(A;;FA;;;SY)",
			wantLen:   2,
			wantFirst: sddlAce{aceType: "D", sidStr: "WD"},
		},
		{
			name:      "inheritable AI accepted as raw type",
			sddl:      "D:P(AI;;FA;;;SY)",
			wantLen:   1,
			wantFirst: sddlAce{aceType: "AI", sidStr: "SY"},
		},
		{
			name:    "no ACE list",
			sddl:    "D:",
			wantErr: true,
		},
		{
			name:    "empty string",
			sddl:    "",
			wantErr: true,
		},
		{
			name:    "ACE with too few fields",
			sddl:    "D:P(A;;FA;;)",
			wantErr: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := parseDACLAces(tc.sddl)
			if tc.wantErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Len(t, got, tc.wantLen)
			assert.Equal(t, tc.wantFirst, got[0])
		})
	}
}

func TestIsAllowACEType(t *testing.T) {
	t.Parallel()
	cases := []struct {
		t    string
		want bool
	}{
		{"A", true},
		{"AI", true},
		{"AO", true},
		{"D", false},
		{"DI", false},
		{"AU", false},
		{"AL", false},
		{"OA", false},
		{"", false},
	}
	for _, c := range cases {
		t.Run(c.t, func(t *testing.T) {
			t.Parallel()
			if got := isAllowACEType(c.t); got != c.want {
				t.Errorf("isAllowACEType(%q) = %v, want %v", c.t, got, c.want)
			}
		})
	}
}
