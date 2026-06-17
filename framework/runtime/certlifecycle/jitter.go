package certlifecycle

import (
	"crypto/sha256"
	"encoding/binary"
	"math"
	"time"
)

// Renewal-jitter model (k8s kubelet / cert-manager 70–90% lifetime): a
// certificate becomes due for renewal at a DETERMINISTIC instant within 70–90%
// of its validity window. The jitter fraction is derived from a hash of the
// certificate identity (deviceID + serial), NOT from the clock or a random
// source, so:
//
//   - the decision is identical on every reconcile tick and on every replica —
//     a leader handoff mid-window does not shift the renewal instant (no
//     flapping, no thundering herd from a fleet renewing at exactly the same %);
//   - it is purely a function of its inputs, so it is fully table-testable.
const (
	// renewalLowerFraction / renewalUpperFraction bound the jittered renewal
	// instant as a fraction of the certificate's (notAfter-notBefore) lifetime.
	renewalLowerFraction = 0.70
	renewalUpperFraction = 0.90
)

// renewalFraction returns the deterministic jitter fraction in
// [renewalLowerFraction, renewalUpperFraction) for the given certificate
// identity. The NUL separator between deviceID and serial makes the input
// unambiguous, so ("ab","c") and ("a","bc") hash distinctly.
func renewalFraction(deviceID, serial string) float64 {
	h := sha256.Sum256([]byte(deviceID + "\x00" + serial))
	u := binary.BigEndian.Uint64(h[:8])
	// u / 2^64 is uniform in [0,1); scaling lands in [lower, upper).
	frac := float64(u) / (float64(math.MaxUint64) + 1)
	return renewalLowerFraction + frac*(renewalUpperFraction-renewalLowerFraction)
}

// renewalInstant returns the absolute time the certificate becomes due for
// renewal: notBefore + lifetime*renewalFraction. A non-positive lifetime
// (degenerate / malformed validity window) returns notBefore — immediately due —
// rather than producing a time before notBefore.
func renewalInstant(notBefore, notAfter time.Time, deviceID, serial string) time.Time {
	lifetime := notAfter.Sub(notBefore)
	if lifetime <= 0 {
		return notBefore
	}
	offset := time.Duration(float64(lifetime) * renewalFraction(deviceID, serial))
	return notBefore.Add(offset)
}

// dueForRenewal reports whether now has reached the certificate's jittered
// renewal instant. A certificate already past notAfter is necessarily past its
// renewal instant (notAfter > renewalInstant), so this also returns true for an
// expired cert — the Reconciler re-signs it to recover (see reconciler.go).
func dueForRenewal(now, notBefore, notAfter time.Time, deviceID, serial string) bool {
	return !now.Before(renewalInstant(notBefore, notAfter, deviceID, serial))
}
