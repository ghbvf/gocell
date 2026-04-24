// Package setup implements the interactive first-run admin provisioning slice.
//
// Two Public HTTP endpoints:
//
//	GET  /api/v1/setup/status   — returns {"hasAdmin": bool}
//	POST /api/v1/setup/admin    — creates the first admin; 409 after initialized
//
// Race-safe admin creation is delegated to cells/accesscore/internal/adminprovision
// so the semantics match the headless initialadmin Lifecycle exactly.
package setup

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/ghbvf/gocell/cells/accesscore/internal/adminprovision"
	"github.com/ghbvf/gocell/cells/accesscore/internal/domain"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/validation"
)

// TopicUserCreated is the event topic published on fresh admin creation.
// Same contract / topic as identitymanage emits so downstream projections
// treat the first admin as a regular user.created fact.
const TopicUserCreated = "event.user.created.v1"

// UserIDPrefix distinguishes setup-path admins from bootstrap-path admins in
// audit logs ("usr-" vs. "usr-bootstrap-").
const UserIDPrefix = "usr-"

// Password length bounds for the setup endpoint:
//   - MinPasswordLen mirrors the schema's minLength:8 and is low enough to not
//     surprise first-run operators on low-entropy test setups.
//   - MaxPasswordLen caps bcrypt input. bcrypt itself truncates at 72 bytes,
//     so the cap prevents unbounded CPU / memory on adversarial inputs
//     without changing effective security (anything beyond 72 is discarded).
const (
	MinPasswordLen = 8
	MaxPasswordLen = 128
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

// StatusOutput is the response shape for GET /api/v1/setup/status.
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

// CreateAdminOutput is the response shape for POST /api/v1/setup/admin.
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
// without the event emitted. The next POST hits the fast-path 409 via
// CountByRole. Production must wire a real TxRunner; demo mode accepts this
// gap as it matches the identitymanage.Create pattern (service.go:128-139).
func (s *Service) CreateAdmin(ctx context.Context, in CreateAdminInput) (*CreateAdminOutput, error) {
	if err := validation.RequireNotBlank(errcode.ErrAuthIdentityInvalidInput,
		validation.F("username", in.Username),
		validation.F("email", in.Email),
		validation.F("password", in.Password),
	); err != nil {
		return nil, err
	}
	if len(in.Password) < MinPasswordLen || len(in.Password) > MaxPasswordLen {
		return nil, errcode.New(errcode.ErrAuthIdentityInvalidInput,
			fmt.Sprintf("password length must be between %d and %d characters",
				MinPasswordLen, MaxPasswordLen))
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(in.Password), domain.BcryptCost)
	if err != nil {
		return nil, fmt.Errorf("setup: hash password: %w", err)
	}

	var out *CreateAdminOutput
	err = s.txRunner.RunInTx(ctx, func(txCtx context.Context) error {
		user, outcome, perr := s.provisioner.Ensure(txCtx, adminprovision.ProvisionInput{
			Username:     in.Username,
			Email:        in.Email,
			PasswordHash: hash,
			RequireReset: false,
			IDPrefix:     UserIDPrefix,
		})
		if perr != nil {
			return fmt.Errorf("setup: ensure admin: %w", perr)
		}
		switch outcome {
		case adminprovision.OutcomeAlreadyExists, adminprovision.OutcomeRaceSkipped:
			return errcode.New(errcode.ErrSetupAlreadyInitialized,
				"first-run admin already provisioned; use /api/v1/access/sessions/login")
		case adminprovision.OutcomeCreated:
			if err := s.publishUserCreated(txCtx, user); err != nil {
				return err
			}
		case adminprovision.OutcomeOrphanRecovered:
			// Deliberate: do NOT emit user.created on orphan recovery — the
			// crashed prior run presumably emitted it before the credfile
			// failure. Duplicating the event would confuse idempotent consumers
			// that dedupe on (event_type, user_id).
			s.logger.Warn("setup: orphan user recovered; event emission skipped",
				slog.String("event", "setup_orphan_recover"),
				slog.String("user_id", user.ID))
		default:
			return fmt.Errorf("setup: unexpected provision outcome %d", outcome)
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

func (s *Service) publishUserCreated(ctx context.Context, user *domain.User) error {
	payload, err := json.Marshal(map[string]any{
		"user_id":  user.ID,
		"username": user.Username,
	})
	if err != nil {
		return fmt.Errorf("setup: marshal user.created payload: %w", err)
	}
	entry := outbox.Entry{
		ID:        outbox.NewEntryID(),
		EventType: TopicUserCreated,
		Payload:   payload,
	}
	if err := s.emitter.Emit(ctx, entry); err != nil {
		return fmt.Errorf("setup: emit user.created: %w", err)
	}
	return nil
}
