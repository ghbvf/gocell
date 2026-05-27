// Package setup implements the interactive first-run admin provisioning slice.
//
// Two Public HTTP endpoints:
//
//	GET  /api/v1/access/setup/status   — returns {"hasAdmin": bool}
//	POST /api/v1/access/setup/admin    — creates the first admin; 410 Gone after initialized
//
// Race-safe admin creation is delegated to cells/accesscore/internal/adminprovision
// so the semantics match the headless initialadmin Lifecycle exactly.
package setup

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ghbvf/gocell/cells/accesscore/internal/adminprovision"
	"github.com/ghbvf/gocell/cells/accesscore/internal/credential"
	"github.com/ghbvf/gocell/cells/accesscore/internal/domain"
	"github.com/ghbvf/gocell/cells/accesscore/internal/dto"
	"github.com/ghbvf/gocell/cells/accesscore/internal/ports"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/validation"
)

// Password bounds for the setup endpoint:
//   - Password bytes must be printable ASCII so JSON Schema maxLength and
//     bcrypt's byte-counted input limit have the same semantics.
//   - MaxPasswordBytes matches golang.org/x/crypto/bcrypt's hard input limit.
const (
	MaxUsernameLen   = 128
	MaxEmailLen      = 256
	MinPasswordBytes = 8
	MaxPasswordBytes = 72
)

// Option configures a Service.
type Option func(*Service)

// WithEmitter sets the event emitter. Accepts a sealed outbox.CellEmitter;
// typed-nil inputs are silently ignored (builder-option semantics).
func WithEmitter(e outbox.CellEmitter) Option {
	return func(s *Service) {
		if e != nil {
			s.emitter = e
		}
	}
}

// WithTxManager sets the CellTxManager for L2 atomicity (user write + event
// emit). Callers obtain the sealed marker via persistence.WrapForCell from a
// composition root.
func WithTxManager(tx persistence.CellTxManager) Option {
	return func(s *Service) {
		if tx != nil {
			s.txRunner = tx
		}
	}
}

// WithSetupLock injects the REQUIRED serialization primitive for the
// admin-provisioning path. CreateAdmin acquires the lock at the start of the
// RunInTx body before calling adminprovision.Ensure — the lock, user write,
// and outbox emit share a single transaction scope.
//
// NewService rejects a missing or nil setupLock with ErrCellInvalidConfig so
// that mis-wired assemblies fail at startup rather than at the first
// CreateAdmin call. The cell-level WithSetupLock injects this option from
// cells/accesscore composition; see accesscore.WithSetupLock godoc for the
// PG vs memstore wiring choice.
//
// Bare-nil inputs are silently ignored (builder-option semantics); final
// nil validation (including typed-nil) is handled by validateRequired().
func WithSetupLock(lock ports.SetupLockAcquirer) Option {
	return func(s *Service) {
		if lock == nil {
			return
		}
		s.setupLock = lock
	}
}

// WithPasswordHasher overrides the password hasher (default
// credential.NewProductionHasher(), cost 12). Unit tests wire
// credential.NewTestHasher(bcrypt.MinCost) for speed. BCRYPT-COST-FUNNEL-01
// guards that production never reaches the low-cost door. Bare/typed-nil inputs
// are silently ignored (builder-option semantics) so the production default
// survives.
func WithPasswordHasher(h credential.Hasher) Option {
	return func(s *Service) {
		if validation.IsNilInterface(h) {
			return
		}
		s.hasher = h
	}
}

// Service implements the setup slice's business logic.
type Service struct {
	provisioner *adminprovision.Provisioner `gocell:"required" gocellErr:"setup: provisioner is required"` //nolint:lll // R2-approved: struct tag for required-dep funnel cannot be split
	logger      *slog.Logger                `gocell:"required" gocellErr:"setup: logger is required"`
	txRunner    persistence.CellTxManager   `gocell:"required" gocellErr:"setup: TxRunner required; use WithTxManager"`
	emitter     outbox.CellEmitter
	clk         clock.Clock
	// setupLock is the REQUIRED serialization primitive for the admin-provisioning
	// path. CreateAdmin acquires it inside RunInTx before calling
	// provisioner.Ensure. PG mode uses pg_advisory_xact_lock (cross-pod);
	// memstore mode uses accesscore.NoopSetupLock{} because memTxRunner.RunInTx
	// already serializes goroutines via store.mu. NewService rejects nil.
	setupLock ports.SetupLockAcquirer `gocell:"required" gocellErr:"setup: setupLock required; use WithSetupLock — PG callers wire accesspg.NewBundle(pool, txm, clk).SetupLock(), memstore callers wire accesscore.NoopSetupLock{}"` //nolint:lll // R2-approved: struct tag for required-dep funnel cannot be split
	// hasher is the password hasher. Optional, but its default is a SAFE
	// full-strength default — credential.NewProductionHasher() (cost 12) — not a
	// degraded one like emitter's noop; production is correct without explicit
	// wiring. Tests override via WithPasswordHasher(credential.NewTestHasher(
	// bcrypt.MinCost)) for speed. A bare/typed-nil override is ignored, so the
	// default can never be downgraded to nil.
	hasher credential.Hasher
}

// NewService constructs a Service. provisioner is required; passing nil returns
// an error so mis-wired assemblies fail at startup.
func NewService(clk clock.Clock, provisioner *adminprovision.Provisioner, logger *slog.Logger, opts ...Option) (*Service, error) {
	clock.MustHaveClock(clk, "setup.NewService")
	s := &Service{
		provisioner: provisioner,
		emitter:     outbox.DemoCellEmitter(),
		logger:      logger,
		hasher:      credential.NewProductionHasher(),
		clk:         clk,
	}
	for _, o := range opts {
		o(s)
	}
	if err := s.validateRequired(); err != nil {
		return nil, err
	}
	return s, nil
}

// StatusOutput is the response shape for GET /api/v1/access/setup/status.
type StatusOutput struct {
	HasAdmin bool `json:"hasAdmin"`
}

// Status returns whether the system already has at least one admin.
func (s *Service) Status(ctx context.Context) (StatusOutput, error) {
	has, err := s.provisioner.Status(ctx)
	if err != nil {
		return StatusOutput{}, fmt.Errorf("setup: status: %w", err)
	}
	return StatusOutput{HasAdmin: has}, nil
}

// CreateAdminInput holds the operator-supplied first-admin fields.
type CreateAdminInput struct {
	Username string
	Email    string
	Password string
}

// CreateAdminOutput is the response shape for POST /api/v1/access/setup/admin.
type CreateAdminOutput struct {
	ID        string `json:"id"`
	Username  string `json:"username"`
	Email     string `json:"email"`
	CreatedAt string `json:"createdAt"`
}

// CreateAdmin provisions the first admin user with an operator-chosen password.
//
// Returns errcode.ErrSetupAlreadyInitialized when an admin already exists
// (either at the fast-path Status check or after a race detected inside
// adminprovision.Ensure).
//
// Consistency: L2 (OutboxFact) in durable mode. The user write + event emit
// share a single TxRunner scope so event publication is atomic with row
// persistence — if the emit fails, the tx rolls back and the user row is
// removed.
//
// Demo-mode caveat: When wired with persistence.NoopTxRunner (in-memory
// repositories), RunInTx has no rollback, so a publishUserCreated failure
// after a successful adminprovision.Ensure leaves the user + role persisted
// without the event emitted. The next POST hits the fast-path 410 via
// CountByRole. Production must wire a real TxRunner; demo mode accepts this
// gap as it matches the identitymanage.Create pattern (service.go:128-139).
//
// Security: bcrypt runs AFTER the Status fast-path so a flood of POSTs after
// admin exists returns 410 in ~milliseconds without CPU burn. The hash cost
// (credential.ProductionCost in production) is only paid on the single winning
// request (plus same-process concurrent race-losers serialized by
// memTxRunner.RunInTx holding store.mu in memstore mode, or by
// pg_advisory_xact_lock in PG mode).
func (s *Service) CreateAdmin(ctx context.Context, in CreateAdminInput) (*CreateAdminOutput, error) {
	if err := validateCreateAdminInput(in); err != nil {
		return nil, err
	}

	// Fast-path: if admin already exists, return 410 without touching bcrypt.
	// This keeps anonymous floods on the retired endpoint in O(1) roundtrip.
	hasAdmin, err := s.provisioner.Status(ctx)
	if err != nil {
		return nil, fmt.Errorf("setup: status: %w", err)
	}
	if hasAdmin {
		return nil, setupRetiredError()
	}

	hash, err := s.hasher.Hash([]byte(in.Password))
	if err != nil {
		return nil, fmt.Errorf("setup: hash password: %w", err)
	}

	var out *CreateAdminOutput
	err = s.txRunner.RunInTx(ctx, func(txCtx context.Context) error {
		// Acquire the setup lock first so that the CountByRole==0 fast-path,
		// user write, and outbox emit all run under the same serialization
		// boundary. PG mode uses pg_advisory_xact_lock (cross-pod); memstore
		// mode uses NoopSetupLock — memTxRunner.RunInTx itself holds store.mu
		// for the whole closure, already serializing within-process goroutines.
		// NewService rejects nil at construction so this call is always safe.
		if err := s.setupLock.Acquire(txCtx); err != nil {
			return fmt.Errorf("setup: acquire setup lock: %w", err)
		}
		user, err := s.provisionAndMaybeEmit(txCtx, in, []byte(hash))
		if err != nil {
			return err
		}
		out = &CreateAdminOutput{
			ID:        user.ID,
			Username:  user.Username,
			Email:     user.Email,
			CreatedAt: user.CreatedAt.UTC().Format(time.RFC3339Nano),
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// validateCreateAdminInput enforces blank/length/control-char rules before any
// persistence or CPU-expensive work happens. Pulled out of CreateAdmin to keep
// its cognitive complexity within 15 (gocognit CLAUDE.md limit).
func validateCreateAdminInput(in CreateAdminInput) error {
	if err := validation.RequireNotEmpty(errcode.ErrAuthIdentityInvalidInput,
		validation.F("username", in.Username),
		validation.F("email", in.Email),
		validation.F("password", in.Password),
	); err != nil {
		return err
	}
	if utf8.RuneCountInString(in.Username) > MaxUsernameLen {
		return errcode.New(errcode.KindInvalid, errcode.ErrAuthIdentityInvalidInput,
			"username too long",
			errcode.WithDetails(errcode.PublicInt("max", MaxUsernameLen)))
	}
	if utf8.RuneCountInString(in.Email) > MaxEmailLen {
		return errcode.New(errcode.KindInvalid, errcode.ErrAuthIdentityInvalidInput,
			"email too long",
			errcode.WithDetails(errcode.PublicInt("max", MaxEmailLen)))
	}
	passwordBytes := len(in.Password)
	if passwordBytes < MinPasswordBytes || passwordBytes > MaxPasswordBytes {
		return errcode.New(errcode.KindInvalid, errcode.ErrAuthIdentityInvalidInput,
			"password length out of range",
			errcode.WithDetails(errcode.PublicInt("min", MinPasswordBytes), errcode.PublicInt("max", MaxPasswordBytes)))
	}
	if !isPrintableASCII(in.Password) {
		return errcode.New(errcode.KindInvalid, errcode.ErrAuthIdentityInvalidInput,
			"password must contain only printable ASCII characters")
	}
	if strings.ContainsAny(in.Email, "\r\n\t\x00") || strings.ContainsAny(in.Username, "\r\n\t\x00") {
		return errcode.New(errcode.KindInvalid, errcode.ErrAuthIdentityInvalidInput,
			"username and email must not contain control characters")
	}
	return nil
}

func isPrintableASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < 0x20 || s[i] > 0x7e {
			return false
		}
	}
	return true
}

// provisionAndMaybeEmit runs adminprovision.Ensure inside the caller-provided
// tx and emits user.created on freshly created or recovered pending setup rows.
// Extracted from CreateAdmin to keep CreateAdmin under the cognitive-complexity
// ceiling after adding the pre-bcrypt Status fast-path.
func (s *Service) provisionAndMaybeEmit(ctx context.Context, in CreateAdminInput, hash []byte) (*domain.User, error) {
	result, err := s.provisioner.Ensure(ctx, adminprovision.ProvisionInput{
		Username:     in.Username,
		Email:        in.Email,
		PasswordHash: hash,
		RequireReset: false,
	})
	if err != nil {
		return nil, fmt.Errorf("setup: ensure admin: %w", err)
	}
	switch result.Outcome {
	case adminprovision.OutcomeAlreadyExists, adminprovision.OutcomeRaceSkipped:
		return nil, setupRetiredError()
	case adminprovision.OutcomeCreated:
		if err := s.publishUserCreated(ctx, result.User); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("setup: unexpected provision outcome %d", result.Outcome)
	}
	return result.User, nil
}

// setupRetiredError is returned when the first-run admin already exists. It
// maps to HTTP 410 Gone (see pkg/httputil) — the endpoint is not just
// temporarily conflicting, it is permanently retired for the lifetime of this
// deployment. The details payload carries a semantic next-action only; the
// login endpoint path is resolved by clients via OpenAPI / contract registry,
// not embedded on the wire — contract is the single source of truth for
// endpoint paths.
func setupRetiredError() error {
	return errcode.New(
		errcode.KindGone,
		errcode.ErrSetupAlreadyInitialized,
		"first-run admin already provisioned; this endpoint is retired",
		errcode.WithDetails(errcode.PublicString("nextAction", "login")),
	)
}

func (s *Service) publishUserCreated(ctx context.Context, user *domain.User) error {
	// First-run admin bootstrap has no caller principal yet — by definition
	// this is the very first admin. Audit chain attributes the action to the
	// sentinel actor "system".
	payload, err := json.Marshal(dto.UserCreatedEvent{
		UserID:   user.ID,
		Username: user.Username,
		ActorID:  "system",
	})
	if err != nil {
		return fmt.Errorf("setup: marshal user.created payload: %w", err)
	}
	entry := outbox.Entry{
		ID:         outbox.MustNewEntryID(),
		EventType:  dto.TopicUserCreated,
		Payload:    payload,
		OccurredAt: s.clk.Now().UTC(),
	}
	if err := s.emitter.Emit(ctx, entry); err != nil {
		return fmt.Errorf("setup: emit user.created: %w", err)
	}
	return nil
}
