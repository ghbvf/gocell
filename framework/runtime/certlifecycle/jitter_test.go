package certlifecycle

import (
	"testing"
	"time"
)

// jitterTestLifetime is the cert validity span used by the renewal-instant tests
// (TEST-TIME-LITERAL-01: no inline test-time literals).
const jitterTestLifetime = 100 * time.Hour

func TestRenewalFractionDeterministicAndBounded(t *testing.T) {
	t.Parallel()
	ids := []struct{ device, serial string }{
		{"dev-1", "aa"},
		{"dev-1", "bb"},
		{"dev-2", "aa"},
		{"00000000-0000-0000-0000-000000000001", "deadbeef"},
		{"", ""},
		{"x", ""},
		{"", "x"},
	}
	for _, id := range ids {
		f1 := renewalFraction(id.device, id.serial)
		f2 := renewalFraction(id.device, id.serial)
		if f1 != f2 {
			t.Errorf("renewalFraction(%q,%q) not deterministic: %v != %v", id.device, id.serial, f1, f2)
		}
		if f1 < renewalLowerFraction || f1 >= renewalUpperFraction {
			t.Errorf("renewalFraction(%q,%q) = %v, want [%v,%v)", id.device, id.serial,
				f1, renewalLowerFraction, renewalUpperFraction)
		}
	}
}

func TestRenewalFractionSeparatorUnambiguous(t *testing.T) {
	t.Parallel()
	// The NUL separator must make ("ab","c") distinct from ("a","bc").
	if renewalFraction("ab", "c") == renewalFraction("a", "bc") {
		t.Error("renewalFraction collides across ambiguous concat boundary — separator missing")
	}
}

func TestRenewalFractionSpread(t *testing.T) {
	t.Parallel()
	// Sanity: distinct identities spread across the band rather than clustering at
	// a single value. Bucket 200 ids into 4 quartiles of [0.70,0.90); expect every
	// quartile populated.
	buckets := map[int]int{}
	for i := 0; i < 200; i++ {
		f := renewalFraction("device", string(rune('A'+i%26))+time.Duration(i).String())
		q := int((f - renewalLowerFraction) / ((renewalUpperFraction - renewalLowerFraction) / 4))
		if q == 4 { // f just under upper bound
			q = 3
		}
		buckets[q]++
	}
	for q := 0; q < 4; q++ {
		if buckets[q] == 0 {
			t.Errorf("renewalFraction quartile %d empty — distribution clustered: %v", q, buckets)
		}
	}
}

func TestRenewalInstantAndDue(t *testing.T) {
	t.Parallel()
	notBefore := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	notAfter := notBefore.Add(jitterTestLifetime)
	const dev, serial = "dev", "ser"

	lifetime := notAfter.Sub(notBefore) // runtime value: avoids constant float→Duration conversion
	inst := renewalInstant(notBefore, notAfter, dev, serial)
	frac := renewalFraction(dev, serial)
	wantOffset := time.Duration(float64(lifetime) * frac)
	if got := inst.Sub(notBefore); got != wantOffset {
		t.Errorf("renewalInstant offset = %v, want %v", got, wantOffset)
	}
	// Instant must fall strictly inside the 70–90% window.
	lo := notBefore.Add(time.Duration(float64(lifetime) * renewalLowerFraction))
	hi := notBefore.Add(time.Duration(float64(lifetime) * renewalUpperFraction))
	if inst.Before(lo) || !inst.Before(hi) {
		t.Errorf("renewalInstant %v outside [%v,%v)", inst, lo, hi)
	}

	cases := []struct {
		name string
		now  time.Time
		want bool
	}{
		{"before instant", inst.Add(-time.Nanosecond), false},
		{"exactly at instant", inst, true},
		{"after instant", inst.Add(time.Hour), true},
		{"past notAfter (expired)", notAfter.Add(time.Hour), true},
		{"at notBefore", notBefore, false},
	}
	for _, tc := range cases {
		if got := dueForRenewal(tc.now, notBefore, notAfter, dev, serial); got != tc.want {
			t.Errorf("%s: dueForRenewal = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestRenewalInstantDegenerateLifetime(t *testing.T) {
	t.Parallel()
	nb := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	// notAfter <= notBefore → immediately due, instant == notBefore.
	for _, na := range []time.Time{nb, nb.Add(-time.Hour)} {
		if got := renewalInstant(nb, na, "d", "s"); !got.Equal(nb) {
			t.Errorf("renewalInstant(degenerate na=%v) = %v, want notBefore %v", na, got, nb)
		}
		if !dueForRenewal(nb, nb, na, "d", "s") {
			t.Errorf("dueForRenewal(degenerate na=%v) = false, want true", na)
		}
	}
}
