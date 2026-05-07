package metautil_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/kernel/metautil"
	"github.com/ghbvf/gocell/pkg/errcode"
)

type validateCase struct {
	name      string
	metadata  map[string]string
	prefix    string
	wantOK    bool
	wantInMsg string
}

func TestValidateLimits(t *testing.T) {
	t.Parallel()
	for _, tc := range buildValidateLimitsCases() {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assertValidateCase(t, tc)
		})
	}
}

// assertValidateCase runs ValidateLimits with the case inputs and verifies
// the expected outcome. Extracted out of TestValidateLimits so the test
// function stays under the cognitive-complexity ceiling (≤ 15) while the
// table itself stays exhaustive.
func assertValidateCase(t *testing.T, tc validateCase) {
	t.Helper()
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
}

func buildValidateLimitsCases() []validateCase {
	cases := []validateCase{
		{name: "nil passes", metadata: nil, prefix: metautil.DomainOutbox, wantOK: true},
		{name: "empty passes", metadata: map[string]string{}, prefix: metautil.DomainOutbox, wantOK: true},
		{name: "small valid passes", metadata: map[string]string{"k": "v"}, prefix: metautil.DomainOutbox, wantOK: true},
		{
			name:     "exactly MaxMetadataKeys passes",
			metadata: makeKeys(metautil.MaxMetadataKeys), prefix: metautil.DomainOutbox, wantOK: true,
		},
		{
			name:     "exactly MaxMetadataKeyLen passes",
			metadata: map[string]string{strings.Repeat("k", metautil.MaxMetadataKeyLen): "v"},
			prefix:   metautil.DomainOutbox, wantOK: true,
		},
		{
			name:     "exactly MaxMetadataValueLen passes",
			metadata: map[string]string{"k": strings.Repeat("v", metautil.MaxMetadataValueLen)},
			prefix:   metautil.DomainOutbox, wantOK: true,
		},
	}
	cases = append(cases, perPrefixOverLimitCases()...)
	return cases
}

// perPrefixOverLimitCases yields one over-limit case per (error type ×
// domain prefix) pair so both DomainOutbox and DomainCommand exercise
// every error branch in ValidateLimits.
func perPrefixOverLimitCases() []validateCase {
	bigKey := strings.Repeat("k", metautil.MaxMetadataKeyLen+1)
	bigVal := strings.Repeat("v", metautil.MaxMetadataValueLen+1)
	prefixes := []struct {
		domain string
		label  string
	}{
		{metautil.DomainOutbox, "outbox"},
		{metautil.DomainCommand, "command"},
	}
	var out []validateCase
	for _, p := range prefixes {
		out = append(out,
			validateCase{
				name:      "key count over limit fails with " + p.label + " prefix",
				metadata:  makeKeys(metautil.MaxMetadataKeys + 1),
				prefix:    p.domain,
				wantInMsg: p.label + ": metadata key count exceeds max",
			},
			validateCase{
				name:      "key length over limit fails with " + p.label + " prefix",
				metadata:  map[string]string{bigKey: "v"},
				prefix:    p.domain,
				wantInMsg: p.label + ": metadata key length exceeds max",
			},
			validateCase{
				name:      "value length over limit fails with " + p.label + " prefix",
				metadata:  map[string]string{"k": bigVal},
				prefix:    p.domain,
				wantInMsg: p.label + ": metadata value length exceeds max",
			},
			validateCase{
				name:      "total size over limit fails with " + p.label + " prefix",
				metadata:  makeBigPayload(),
				prefix:    p.domain,
				wantInMsg: p.label + ": metadata total size exceeds max",
			},
		)
	}
	return out
}

func TestValidateLimits_UnknownPrefixReturnsAssertion(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		prefix string
	}{
		{"empty prefix", ""},
		{"unknown prefix", "relay"},
		{"caps mismatch", "Outbox"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assertUnknownPrefixCase(t, tc.prefix)
		})
	}
}

func assertUnknownPrefixCase(t *testing.T, prefix string) {
	t.Helper()
	err := metautil.ValidateLimits(map[string]string{"k": "v"}, prefix)
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	var ec *errcode.Error
	if !errors.As(err, &ec) {
		t.Fatalf("expected errcode.Error, got %T: %v", err, err)
	}
	if ec.Kind != errcode.KindInternal {
		t.Fatalf("expected KindInternal, got %v", ec.Kind)
	}
	if !strings.Contains(ec.Message, "unknown domain prefix") {
		t.Fatalf("expected assertion message, got %q", ec.Message)
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
