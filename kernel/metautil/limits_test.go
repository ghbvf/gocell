package metautil_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/kernel/metautil"
	"github.com/ghbvf/gocell/pkg/errcode"
)

func TestValidateLimits(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		metadata  map[string]string
		prefix    string
		wantOK    bool
		wantInMsg string
	}{
		{name: "nil passes", metadata: nil, prefix: "outbox", wantOK: true},
		{name: "empty passes", metadata: map[string]string{}, prefix: "outbox", wantOK: true},
		{name: "small valid passes", metadata: map[string]string{"k": "v"}, prefix: "outbox", wantOK: true},
		{
			name:     "exactly MaxMetadataKeys passes",
			metadata: makeKeys(metautil.MaxMetadataKeys), prefix: "outbox", wantOK: true,
		},
		{
			name:     "exactly MaxMetadataKeyLen passes",
			metadata: map[string]string{strings.Repeat("k", metautil.MaxMetadataKeyLen): "v"},
			prefix:   "outbox", wantOK: true,
		},
		{
			name:     "exactly MaxMetadataValueLen passes",
			metadata: map[string]string{"k": strings.Repeat("v", metautil.MaxMetadataValueLen)},
			prefix:   "outbox", wantOK: true,
		},
		{
			name:      "key count over limit fails with prefix",
			metadata:  makeKeys(metautil.MaxMetadataKeys + 1),
			prefix:    "outbox",
			wantOK:    false,
			wantInMsg: "outbox: metadata key count exceeds max",
		},
		{
			name:      "key length over limit fails with prefix",
			metadata:  map[string]string{strings.Repeat("k", metautil.MaxMetadataKeyLen+1): "v"},
			prefix:    "command",
			wantOK:    false,
			wantInMsg: "command: metadata key length exceeds max",
		},
		{
			name:      "value length over limit fails with prefix",
			metadata:  map[string]string{"k": strings.Repeat("v", metautil.MaxMetadataValueLen+1)},
			prefix:    "command",
			wantOK:    false,
			wantInMsg: "command: metadata value length exceeds max",
		},
		{
			name:      "total size over limit fails with prefix",
			metadata:  makeBigPayload(),
			prefix:    "outbox",
			wantOK:    false,
			wantInMsg: "outbox: metadata total size exceeds max",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := metautil.ValidateLimits(tc.metadata, tc.prefix)
			if tc.wantOK {
				if err != nil {
					t.Fatalf("expected nil error, got %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error, got nil")
			}
			var ec *errcode.Error
			if !errors.As(err, &ec) {
				t.Fatalf("expected errcode.Error, got %T: %v", err, err)
			}
			if !strings.Contains(ec.Message, tc.wantInMsg) {
				t.Fatalf("expected message to contain %q, got %q", tc.wantInMsg, ec.Message)
			}
		})
	}
}

func TestTruncate(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		in   string
		n    int
		want string
	}{
		{"short unchanged", "abc", 5, "abc"},
		{"exact unchanged", "abcde", 5, "abcde"},
		{"long truncated", "abcdef", 5, "abcde..."},
		{"empty unchanged", "", 5, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := metautil.Truncate(tc.in, tc.n); got != tc.want {
				t.Fatalf("Truncate(%q, %d) = %q, want %q", tc.in, tc.n, got, tc.want)
			}
		})
	}
}

func makeKeys(n int) map[string]string {
	m := make(map[string]string, n)
	for i := 0; i < n; i++ {
		m[keyN(i)] = "v"
	}
	return m
}

func keyN(i int) string {
	return "k" + intToStr(i)
}

func intToStr(i int) string {
	if i == 0 {
		return "0"
	}
	digits := []byte{}
	for i > 0 {
		digits = append([]byte{byte('0' + i%10)}, digits...)
		i /= 10
	}
	return string(digits)
}

func makeBigPayload() map[string]string {
	keyLen := metautil.MaxMetadataKeyLen
	valLen := metautil.MaxMetadataValueLen
	pairs := metautil.MaxMetadataTotalSize/(keyLen+valLen) + 2
	m := make(map[string]string, pairs)
	for i := 0; i < pairs; i++ {
		key := strings.Repeat("a", keyLen-len(intToStr(i))) + intToStr(i)
		m[key] = strings.Repeat("v", valLen)
	}
	return m
}
