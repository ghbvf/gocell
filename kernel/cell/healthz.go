package cell

import (
	"github.com/ghbvf/gocell/kernel/healthz"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/pkg/validation"
)

// RegisterEmitterHealthProbes registers every probe exposed by an emitter that
// also satisfies healthz.ProbeSet (in practice outbox.DirectEmitter, which
// exposes a fail-open-rate probe per cell). It is the single sanctioned funnel
// for emitter health probes across all cells — handwritten cell Init code calls
// this instead of re-deriving the type assertion + registration loop, and cells/
// packages may not call reg.Healthz() directly (HEALTHZ-TYPED-REGISTER-01).
//
// Behavior:
//   - bare-nil or typed-nil emitter → no-op (validation.IsNilInterface, the
//     kernel/runtime single-source typed-nil helper). Guarding before the type
//     assertion is what makes a typed-nil *DirectEmitter safe — calling Probes()
//     on it would panic.
//   - emitter that is not a healthz.ProbeSet → no-op (e.g. WriterEmitter).
//   - emitter that is a ProbeSet → each Probe is registered; the first
//     Aggregator.Register error (e.g. healthz.ErrDuplicateProbe) is returned.
//
// This helper forwards ps.Probes() as-is and does NOT construct probe names:
// emitter probe names ("outbox_failopen_rate_<cell>") stay bare strings owned by
// the emitter, intentionally outside the kernel/healthz.ReadyProbeName funnel
// (see that type's godoc), so this is orthogonal to OPS-CONTRACT-STRING-FUNNEL-01.
//
// AI-robust rating: the funnel inherits the existing healthz registration funnel
// — downstream Medium (HEALTHZ-TYPED-REGISTER-01 keeps cells/ off reg.Healthz();
// HEALTHZ-WRITE-01/A2 caller allowlist now covers kernel/), upstream Medium
// (caller allowlist, not type-system-sealed). The upstream Hard upgrade is
// HEALTHZ-HOLDER-SEAL-01 (seal the Aggregator interface), tracked separately and
// orthogonal to this dedup.
func RegisterEmitterHealthProbes(reg Registrar, emitter outbox.Emitter) error {
	if validation.IsNilInterface(emitter) {
		return nil
	}
	ps, ok := emitter.(healthz.ProbeSet)
	if !ok {
		return nil
	}
	for _, p := range ps.Probes() {
		if err := reg.Healthz().Register(p); err != nil {
			return err
		}
	}
	return nil
}
