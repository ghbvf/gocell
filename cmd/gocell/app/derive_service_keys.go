package app

// derive_service_keys.go — implements `gocell derive-service-keys`.
//
// Derives per-cell signing and verify subkeys from the HMAC master secret and
// emits a shell-eval-able env block for use in split (per-process) deployments.
// The master secret must NOT be forwarded to the split cell process; only the
// derived subkeys are provisioned.
//
// Usage:
//
//	GOCELL_SERVICE_SECRET=<master> \
//	  gocell derive-service-keys --cell <id> [--callers <comma-list>]
//
// The --callers flag lists every cell that calls THIS cell's internal endpoints
// (the callers it must verify). Automatic inference from contract metadata is
// intentionally NOT implemented — the tool is a security provisioning primitive,
// so operators enumerate callers explicitly at deploy time. It is optional only
// for a cell that serves no internal endpoints (accepts no inbound calls): when
// omitted, the tool prints a notice to stderr (so an accidental omission — which
// would yield a keyring that rejects every inbound caller — is visible, not silent).

import (
	"context"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/ghbvf/gocell/framework/kernel/metadata"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/runtime/auth"
)

// runDeriveServiceKeys implements: gocell derive-service-keys --cell <id> [--callers <comma-list>].
func runDeriveServiceKeys(_ context.Context, args []string) error {
	fs := flag.NewFlagSet("derive-service-keys", flag.ContinueOnError)
	cellID := fs.String("cell", "", "cell id to derive keys for (required)")
	callers := fs.String("callers", "",
		"comma-separated caller cell ids this cell verifies on its internal listener "+
			"(omit only if this cell serves no internal endpoints)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if *cellID == "" {
		_ = fs.Usage
		return fmt.Errorf("derive-service-keys: --cell is required\n" +
			"  Usage: gocell derive-service-keys --cell <id> [--callers <comma-list>]\n" +
			"  Run 'gocell derive-service-keys -h' for help")
	}

	callerList := parseCallerList(*callers)
	// Validate operator input at the CLI boundary BEFORE touching the master
	// secret, so malformed --cell/--callers classify as input errors (#2153 F4).
	if err := validateDeriveInput(*cellID, callerList); err != nil {
		return err
	}

	master, err := loadMasterKeyRing()
	if err != nil {
		return err
	}

	if len(callerList) == 0 {
		// Make an empty caller set visible: the resulting keyring verifies NO
		// inbound caller. Legitimate only for an outbound-only cell; otherwise a
		// forgotten --callers would silently 401 every internal call at runtime.
		fmt.Fprintf(os.Stderr,
			"note: no --callers given; %q will accept NO inbound internal calls "+
				"(GOCELL_SERVICE_VERIFY_KEYS will be empty)\n", *cellID)
	}

	pk, err := auth.DeriveProvisionedKeys(master, *cellID, callerList)
	if err != nil {
		return errcode.Wrap(errcode.KindInternal, errcode.ErrAuthKeyInvalid,
			"derive provisioned keys", err)
	}

	return emitProvisionedEnv(os.Stdout, pk)
}

// validateDeriveInput validates operator-supplied CLI input (the --cell id and
// each --callers entry) at the CLI boundary. A malformed value is classified as
// KindInvalid / ERR_AUTH_INVALID_INPUT — a 4xx-class operator input error,
// distinct from the KindInternal / ERR_AUTH_KEY_INVALID that signals corrupt key
// material (#2153 F4). This lets scripts and automation route usage errors apart
// from genuine internal key failures. The offending value is carried as a public
// detail so it surfaces to the operator (these are operator-typed CLI args, not
// secrets).
func validateDeriveInput(cellID string, callers []string) error {
	if !metadata.MatchCellID(cellID) {
		return errcode.New(errcode.KindInvalid, errcode.ErrAuthInvalidInput,
			"derive-service-keys: --cell is not a valid cell id",
			errcode.WithDetails(errcode.PublicString("cell", cellID)))
	}
	for _, c := range callers {
		if !metadata.MatchCellID(c) {
			return errcode.New(errcode.KindInvalid, errcode.ErrAuthInvalidInput,
				"derive-service-keys: --callers contains an invalid cell id",
				errcode.WithDetails(errcode.PublicString("caller", c)))
		}
	}
	return nil
}

// loadMasterKeyRing reads GOCELL_SERVICE_SECRET (required) and
// GOCELL_SERVICE_SECRET_PREVIOUS (optional) from the environment and constructs
// an HMACKeyRing. Returns a descriptive error if the required variable is absent.
func loadMasterKeyRing() (*auth.HMACKeyRing, error) {
	current := os.Getenv(auth.EnvServiceSecret)
	if current == "" {
		return nil, errcode.New(errcode.KindInternal, errcode.ErrAuthKeyMissing,
			"master key env not set",
			errcode.WithDetails(errcode.PublicString("env", auth.EnvServiceSecret)))
	}

	var previous []byte
	if prev := os.Getenv(auth.EnvServiceSecretPrevious); prev != "" {
		previous = []byte(prev)
	}

	ring, err := auth.NewHMACKeyRing([]byte(current), previous)
	if err != nil {
		return nil, errcode.Wrap(errcode.KindInternal, errcode.ErrAuthKeyInvalid,
			"construct master HMAC key ring", err)
	}
	return ring, nil
}

// parseCallerList splits a comma-separated caller list, trimming whitespace and
// dropping empty entries. Returns nil (not empty slice) when callers is empty,
// which is valid for a cell that accepts no inbound internal calls.
func parseCallerList(callers string) []string {
	if strings.TrimSpace(callers) == "" {
		return nil
	}
	raw := strings.Split(callers, ",")
	out := make([]string, 0, len(raw))
	for _, c := range raw {
		c = strings.TrimSpace(c)
		if c != "" {
			out = append(out, c)
		}
	}
	return out
}

// emitProvisionedEnv writes a shell-eval-able env block to w. The block sets:
//
//   - GOCELL_SERVICE_CELL=<cell>
//   - GOCELL_SERVICE_SIGNING_KEY=<hex>
//   - GOCELL_SERVICE_SIGNING_KEY_PREVIOUS=<hex>   (only when non-nil)
//   - GOCELL_SERVICE_VERIFY_KEYS=<callerA:hex,...>
//   - GOCELL_SERVICE_VERIFY_KEYS_PREVIOUS=<...>   (only when non-nil)
func emitProvisionedEnv(w io.Writer, pk auth.ProvisionedKeys) error {
	lines := []string{
		fmt.Sprintf("export %s=%s", auth.EnvServiceOwnCell, pk.OwnCell),
		fmt.Sprintf("export %s=%s", auth.EnvServiceSigningKey, hex.EncodeToString(pk.SigningCurrent)),
	}

	if pk.SigningPrevious != nil {
		lines = append(lines,
			fmt.Sprintf("export %s=%s", auth.EnvServiceSigningKeyPrevious, hex.EncodeToString(pk.SigningPrevious)))
	}

	lines = append(lines,
		fmt.Sprintf("export %s=%s", auth.EnvServiceVerifyKeys, auth.FormatVerifyKeys(pk.VerifyCurrent)))

	if len(pk.VerifyPrevious) > 0 {
		lines = append(lines,
			fmt.Sprintf("export %s=%s", auth.EnvServiceVerifyKeysPrevious, auth.FormatVerifyKeys(pk.VerifyPrevious)))
	}

	for _, l := range lines {
		if _, err := fmt.Fprintln(w, l); err != nil {
			return fmt.Errorf("derive-service-keys: write output: %w", err)
		}
	}
	return nil
}
