package auth

// provisioned_keyring.go — the split (per-cell, master-absent) ServiceKeyring.
//
// In a split deployment each cell runs in its own process. To make cross-cell
// caller-identity forgery cryptographically impossible, a cell process must NOT
// hold the master secret (which derives every cell's subkey). Instead it is
// provisioned — via `gocell derive-service-keys` (run by the operator at deploy
// time) — with only:
//
//   - its own signing subkey(s)  HKDF(master, ownCell)            [current(, previous)]
//   - the verify subkey(s) of its DECLARED callers
//     HKDF(master, callerCell) for callerCell ∈ served clients    [current(, previous)]
//
// A compromise of such a process leaks only those subkeys: the attacker can
// impersonate ownCell (which it already is) and can verify its declared callers,
// but cannot derive any other cell's subkey (no master) — so it cannot forge a
// token claiming a third cell. This is the #2153 Hard property; it holds only
// when the master is ABSENT from the process (HMACKeyRing/monolith does not have
// this property — see its godoc).

import (
	"encoding/hex"
	"fmt"
	"os"
	"sort"
	"strings"

	kauth "github.com/ghbvf/gocell/framework/kernel/auth"
	"github.com/ghbvf/gocell/framework/kernel/metadata"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
)

// Split (per-cell, master-absent) service-token provisioning env vars. These are
// MUTUALLY EXCLUSIVE with EnvServiceSecret (master mode): a cell either derives
// subkeys from a master (monolith) or is provisioned subkeys (split), never both
// — the composition root fails closed when both or neither are set.
const (
	// EnvServiceOwnCell is the cell id this process signs as (split mode).
	EnvServiceOwnCell = "GOCELL_SERVICE_CELL"
	// EnvServiceSigningKey is this cell's current signing subkey, hex-encoded.
	EnvServiceSigningKey = "GOCELL_SERVICE_SIGNING_KEY"
	// EnvServiceSigningKeyPrevious is the optional previous signing subkey (hex)
	// covering a master-rotation overlap window.
	EnvServiceSigningKeyPrevious = "GOCELL_SERVICE_SIGNING_KEY_PREVIOUS"
	// EnvServiceVerifyKeys maps each declared caller to its current verify subkey:
	// "callerA:<hex>,callerB:<hex>".
	EnvServiceVerifyKeys = "GOCELL_SERVICE_VERIFY_KEYS"
	// EnvServiceVerifyKeysPrevious is the optional previous-generation verify map
	// (same encoding) covering a master-rotation overlap window.
	EnvServiceVerifyKeysPrevious = "GOCELL_SERVICE_VERIFY_KEYS_PREVIOUS"
)

// ProvisionedKeyring is the split (per-cell) ServiceKeyring: master-absent, it
// holds only this cell's signing subkey(s) and its declared callers' verify
// subkey(s). See the file header for the security rationale.
type ProvisionedKeyring struct {
	ownCell string
	signing [][]byte            // current[, previous]
	verify  map[string][][]byte // callerCell -> current[, previous]
}

// Compile-time assertion: ProvisionedKeyring implements ServiceKeyring.
var _ kauth.ServiceKeyring = (*ProvisionedKeyring)(nil)

// NewProvisionedKeyring constructs a split keyring. ownCell must be a valid cell
// id; signing must hold at least one subkey (current); verify maps each declared
// caller to its subkeys. All subkeys must be at least MinHMACKeyBytes.
func NewProvisionedKeyring(ownCell string, signing [][]byte, verify map[string][][]byte) (*ProvisionedKeyring, error) {
	r := &ProvisionedKeyring{ownCell: ownCell, signing: signing, verify: verify}
	if err := r.Validate(); err != nil {
		return nil, err
	}
	return r, nil
}

// SigningSecrets returns this cell's signing subkeys. Split processes may sign
// only as themselves: ownCell must equal the provisioned cell identity, else the
// request is rejected fail-closed (a cell must not sign as another cell).
func (r *ProvisionedKeyring) SigningSecrets(ownCell string) ([][]byte, error) {
	if ownCell != r.ownCell {
		return nil, errcode.New(errcode.KindInternal, errcode.ErrAuthKeyInvalid,
			"provisioned keyring may sign only as its own cell",
			errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf("requested=%q own=%q", ownCell, r.ownCell))))
	}
	return cloneSecrets(r.signing), nil
}

// VerifySecrets returns the verify subkeys for callerCell. A caller outside this
// process's declared set has no provisioned subkey → error (fail-closed,
// least-privilege: the callee cannot verify — let alone forge — callers it was
// never authorized to accept).
func (r *ProvisionedKeyring) VerifySecrets(callerCell string) ([][]byte, error) {
	secrets, ok := r.verify[callerCell]
	if !ok {
		return nil, errcode.New(errcode.KindUnauthenticated, errcode.ErrAuthUnauthorized,
			"caller cell is not in this process's provisioned verify set",
			errcode.WithDetails(errcode.PublicString("callerCell", callerCell)))
	}
	return cloneSecrets(secrets), nil
}

// Validate enforces ownCell validity and that every subkey meets MinHMACKeyBytes.
func (r *ProvisionedKeyring) Validate() error {
	if r.ownCell == "" || !metadata.MatchCellID(r.ownCell) {
		return errcode.New(errcode.KindInternal, errcode.ErrAuthKeyInvalid,
			"provisioned keyring ownCell must be a valid cell id",
			errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf("ownCell=%q", r.ownCell))))
	}
	if err := validateSubkeySet("signing", r.signing); err != nil {
		return err
	}
	for caller, secrets := range r.verify {
		if !metadata.MatchCellID(caller) {
			return errcode.New(errcode.KindInternal, errcode.ErrAuthKeyInvalid,
				"provisioned verify map has an invalid caller cell id",
				errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf("caller=%q", caller))))
		}
		if err := validateSubkeySet("verify:"+caller, secrets); err != nil {
			return err
		}
	}
	return nil
}

// validateSubkeySet requires at least one subkey and every subkey to meet
// MinHMACKeyBytes; label identifies the set (e.g. "signing", "verify:<caller>").
func validateSubkeySet(label string, secrets [][]byte) error {
	if len(secrets) == 0 {
		return errcode.New(errcode.KindInternal, errcode.ErrAuthKeyMissing,
			"provisioned subkey set is empty",
			errcode.WithInternal(errcode.InternalAttr("_", "set="+label)))
	}
	for i, s := range secrets {
		if len(s) < MinHMACKeyBytes {
			return errcode.New(errcode.KindInternal, errcode.ErrAuthKeyInvalid,
				"provisioned subkey is too short",
				errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf("set=%s idx=%d got=%d min=%d", label, i, len(s), MinHMACKeyBytes))))
		}
	}
	return nil
}

// LoadProvisionedKeyringFromEnv builds a ProvisionedKeyring from the split env
// vars (EnvServiceOwnCell / EnvServiceSigningKey[_PREVIOUS] /
// EnvServiceVerifyKeys[_PREVIOUS]). Hex decoding and length are validated.
func LoadProvisionedKeyringFromEnv() (*ProvisionedKeyring, error) {
	ownCell := os.Getenv(EnvServiceOwnCell)

	signing, err := decodeHexKeyPair(os.Getenv(EnvServiceSigningKey), os.Getenv(EnvServiceSigningKeyPrevious))
	if err != nil {
		return nil, errcode.Wrap(errcode.KindInternal, errcode.ErrAuthKeyInvalid,
			"decode "+EnvServiceSigningKey, err)
	}

	verifyCur, err := parseVerifyKeys(os.Getenv(EnvServiceVerifyKeys))
	if err != nil {
		return nil, errcode.Wrap(errcode.KindInternal, errcode.ErrAuthKeyInvalid,
			"parse "+EnvServiceVerifyKeys, err)
	}
	verifyPrev, err := parseVerifyKeys(os.Getenv(EnvServiceVerifyKeysPrevious))
	if err != nil {
		return nil, errcode.Wrap(errcode.KindInternal, errcode.ErrAuthKeyInvalid,
			"parse "+EnvServiceVerifyKeysPrevious, err)
	}

	verify := make(map[string][][]byte, len(verifyCur))
	for caller, cur := range verifyCur {
		secrets := [][]byte{cur}
		if prev, ok := verifyPrev[caller]; ok {
			secrets = append(secrets, prev)
		}
		verify[caller] = secrets
	}

	return NewProvisionedKeyring(ownCell, signing, verify)
}

// decodeHexKeyPair decodes a current (required) and optional previous hex key.
func decodeHexKeyPair(curHex, prevHex string) ([][]byte, error) {
	if curHex == "" {
		return nil, fmt.Errorf("current key is empty")
	}
	cur, err := hex.DecodeString(curHex)
	if err != nil {
		return nil, fmt.Errorf("current key: %w", err)
	}
	out := [][]byte{cur}
	if prevHex != "" {
		prev, err := hex.DecodeString(prevHex)
		if err != nil {
			return nil, fmt.Errorf("previous key: %w", err)
		}
		out = append(out, prev)
	}
	return out, nil
}

// parseVerifyKeys parses "callerA:<hex>,callerB:<hex>" into caller→subkey. An
// empty string yields an empty (non-nil) map.
func parseVerifyKeys(s string) (map[string][]byte, error) {
	out := map[string][]byte{}
	if strings.TrimSpace(s) == "" {
		return out, nil
	}
	for _, pair := range strings.Split(s, ",") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		caller, keyHex, ok := strings.Cut(pair, ":")
		if !ok || caller == "" || keyHex == "" {
			return nil, fmt.Errorf("malformed entry %q (want callerCell:hexKey)", pair)
		}
		key, err := hex.DecodeString(keyHex)
		if err != nil {
			return nil, fmt.Errorf("entry %q: %w", caller, err)
		}
		out[caller] = key
	}
	return out, nil
}

// FormatVerifyKeys encodes a caller→subkey map as the EnvServiceVerifyKeys wire
// form ("callerA:<hex>,callerB:<hex>"), callers sorted for determinism. Single
// source shared by the deriver (gocell CLI) and parseVerifyKeys above.
func FormatVerifyKeys(m map[string][]byte) string {
	callers := make([]string, 0, len(m))
	for c := range m {
		callers = append(callers, c)
	}
	sort.Strings(callers)
	parts := make([]string, 0, len(callers))
	for _, c := range callers {
		parts = append(parts, c+":"+hex.EncodeToString(m[c]))
	}
	return strings.Join(parts, ",")
}

// ProvisionedKeys is the derived per-cell key material for one split cell,
// produced by DeriveProvisionedKeys and emitted as env by the
// `gocell derive-service-keys` CLI.
type ProvisionedKeys struct {
	OwnCell         string
	SigningCurrent  []byte
	SigningPrevious []byte            // nil when master has no previous secret
	VerifyCurrent   map[string][]byte // caller -> current verify subkey
	VerifyPrevious  map[string][]byte // caller -> previous verify subkey (nil when no master previous)
}

// DeriveProvisionedKeys derives ownCell's signing subkeys and the verify subkeys
// for each caller from the master ring. It is the single source of derivation
// shared with runtime sign/verify (both call deriveCellSecret), so the keys the
// CLI emits for a split cell are byte-identical to what a master-mode process
// would derive — verification across the two modes always agrees.
func DeriveProvisionedKeys(master *HMACKeyRing, ownCell string, callers []string) (ProvisionedKeys, error) {
	if master == nil {
		return ProvisionedKeys{}, fmt.Errorf("master ring must not be nil")
	}
	if !metadata.MatchCellID(ownCell) {
		return ProvisionedKeys{}, fmt.Errorf("ownCell %q is not a valid cell id", ownCell)
	}
	hasPrev := len(master.previous) > 0

	sigCur, sigPrev, err := master.deriveGenerations(ownCell, hasPrev)
	if err != nil {
		return ProvisionedKeys{}, err
	}
	pk := ProvisionedKeys{OwnCell: ownCell, SigningCurrent: sigCur, SigningPrevious: sigPrev, VerifyCurrent: map[string][]byte{}}
	if hasPrev {
		pk.VerifyPrevious = map[string][]byte{}
	}
	for _, caller := range callers {
		if !metadata.MatchCellID(caller) {
			return ProvisionedKeys{}, fmt.Errorf("caller %q is not a valid cell id", caller)
		}
		vc, vp, err := master.deriveGenerations(caller, hasPrev)
		if err != nil {
			return ProvisionedKeys{}, err
		}
		pk.VerifyCurrent[caller] = vc
		if hasPrev {
			pk.VerifyPrevious[caller] = vp
		}
	}
	return pk, nil
}

// deriveGenerations derives the current (and, when hasPrev, previous) per-cell
// subkey for cellID from the master ring's secrets.
func (r *HMACKeyRing) deriveGenerations(cellID string, hasPrev bool) (cur, prev []byte, err error) {
	cur, err = deriveCellSecret(r.current, cellID)
	if err != nil {
		return nil, nil, err
	}
	if !hasPrev {
		return cur, nil, nil
	}
	prev, err = deriveCellSecret(r.previous, cellID)
	if err != nil {
		return nil, nil, err
	}
	return cur, prev, nil
}

func cloneSecrets(in [][]byte) [][]byte {
	out := make([][]byte, len(in))
	for i, s := range in {
		out[i] = append([]byte(nil), s...)
	}
	return out
}
