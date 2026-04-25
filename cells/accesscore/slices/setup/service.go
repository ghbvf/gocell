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

	"golang.org/x/crypto/bcrypt"

	"github.com/ghbvf/gocell/cells/accesscore/internal/adminprovision"
	"github.com/ghbvf/gocell/cells/accesscore/internal/domain"
	"github.com/ghbvf/gocell/cells/accesscore/internal/dto"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/validation"
)

// UserIDPrefix distinguishes setup-path admins from bootstrap-path admins in
// audit logs ("usr-" vs. "usr-bootstrap-").
const UserIDPrefix = "usr-"

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

// WithEmitter sets the event emitter. Defaults to a Noop emitter.
func WithEmitter(e outbox.Emitter) Option {
	return func(s *Service) {
		if e != nil {
			s.emitter = e
		}
	}
}

// WithTxManager sets the TxRunner for L2 atomicity (user write + event emit).
func WithTxManager(tx persistence.TxRunner) Option {
	return func(s *Service) { s.txRunner = persistence.RunnerOrNoop(tx) }
}

// Service implements the setup slice's business logic.
type Service struct {
	provisioner *adminprovision.Provisioner
	txRunner    persistence.TxRunner
	emitter     outbox.Emitter
	logger      *slog.Logger
}

// NewService constructs a Service. provisioner is required; passing nil returns
// an error so mis-wired assemblies fail at startup.
func NewService(provisioner *adminprovision.Provisioner, logger *slog.Logger, opts ...Option) (*Service, error) {
	if provisioner == nil {
		return nil, fmt.Errorf("setup: provisioner is required")
	}
	if logger == nil {
		return nil, fmt.Errorf("setup: logger is required")
	}
	s := &Service{
		provisioner: provisioner,
		txRunner:    persistence.NoopTxRunner{},
		emitter:     outbox.NewNoopEmitter(),
		logger:      logger,
	}
	for _, o := range opts {
		o(s)
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
// admin exists returns 410 in ~milliseconds without CPU burn. bcrypt cost=12
// is only paid on the single winning request (plus same-process concurrent
// race-losers before the internal mutex in adminprovision serializes them).
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

	hash, err := bcrypt.GenerateFromPassword([]byte(in.Password), domain.BcryptCost)
	if err != nil {
		return nil, fmt.Errorf("setup: hash password: %w", err)
	}

	var out *CreateAdminOutput
	err = s.txRunner.RunInTx(ctx, func(txCtx context.Context) error {
		user, err := s.provisionAndMaybeEmit(txCtx, in, hash)
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
	if err := validation.RequireNotBlank(errcode.ErrAuthIdentityInvalidInput,
		validation.F("username", in.Username),
		validation.F("email", in.Email),
		validation.F("password", in.Password),
	); err != nil {
		return err
	}
	if utf8.RuneCountInString(in.Username) > MaxUsernameLen {
		return errcode.New(errcode.ErrAuthIdentityInvalidInput,
			fmt.Sprintf("username length must be at most %d characters", MaxUsernameLen))
	}
	if utf8.RuneCountInString(in.Email) > MaxEmailLen {
		return errcode.New(errcode.ErrAuthIdentityInvalidInput,
			fmt.Sprintf("email length must be at most %d characters", MaxEmailLen))
	}
	passwordBytes := len(in.Password)
	if passwordBytes < MinPasswordBytes || passwordBytes > MaxPasswordBytes {
		return errcode.New(errcode.ErrAuthIdentityInvalidInput,
			fmt.Sprintf("password length must be %d to %d printable ASCII bytes",
				MinPasswordBytes, MaxPasswordBytes))
	}
	if !isPrintableASCII(in.Password) {
		return errcode.New(errcode.ErrAuthIdentityInvalidInput,
			"password must contain only printable ASCII characters")
	}
	if strings.ContainsAny(in.Email, "\r\n\t\x00") || strings.ContainsAny(in.Username, "\r\n\t\x00") {
		return errcode.New(errcode.ErrAuthIdentityInvalidInput,
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
	user, outcome, err := s.provisioner.Ensure(ctx, adminprovision.ProvisionInput{
		Username:     in.Username,
		Email:        in.Email,
		PasswordHash: hash,
		RequireReset: false,
		IDPrefix:     UserIDPrefix,
		Source:       domain.UserSourceSetup,
	})
	if err != nil {
		return nil, fmt.Errorf("setup: ensure admin: %w", err)
	}
	switch outcome {
	case adminprovision.OutcomeAlreadyExists, adminprovision.OutcomeRaceSkipped:
		return nil, setupRetiredError()
	case adminprovision.OutcomeCreated:
		if err := s.publishUserCreated(ctx, user); err != nil {
			return nil, err
		}
	case adminprovision.OutcomeOrphanRecovered:
		s.logger.Warn("setup: orphan user recovered; emitting user.created",
			slog.String("event", "setup_orphan_recover"),
			slog.String("user_id", user.ID))
		if err := s.publishUserCreated(ctx, user); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("setup: unexpected provision outcome %d", outcome)
	}
	return user, nil
}

// setupRetiredError is returned when the first-run admin already exists. It
// maps to HTTP 410 Gone (see pkg/httputil) — the endpoint is not just
// temporarily conflicting, it is permanently retired for the lifetime of this
// deployment. The details payload carries a semantic next-action only; the
// login endpoint path is resolved by clients via OpenAPI / contract registry,
// not embedded on the wire — contract is the single source of truth for
// endpoint paths.
func setupRetiredError() error {
	return errcode.WithDetails(
		errcode.New(errcode.ErrSetupAlreadyInitialized,
			"first-run admin already provisioned; this endpoint is retired"),
		map[string]any{"nextAction": "login"},
	)
}

func (s *Service) publishUserCreated(ctx context.Context, user *domain.User) error {
	payload, err := json.Marshal(dto.UserCreatedEvent{
		UserID:   user.ID,
		Username: user.Username,
	})
	if err != nil {
		return fmt.Errorf("setup: marshal user.created payload: %w", err)
	}
	entry := outbox.Entry{
		ID:        outbox.NewEntryID(),
		EventType: dto.TopicUserCreated,
		Payload:   payload,
	}
	if err := s.emitter.Emit(ctx, entry); err != nil {
		return fmt.Errorf("setup: emit user.created: %w", err)
	}
	return nil
}
