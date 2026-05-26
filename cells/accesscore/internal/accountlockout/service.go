package accountlockout

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/ghbvf/gocell/cells/accesscore/internal/authzmutate"
	"github.com/ghbvf/gocell/cells/accesscore/internal/domain"
	"github.com/ghbvf/gocell/cells/accesscore/internal/dto"
	"github.com/ghbvf/gocell/cells/accesscore/internal/ports"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/validation"
)

// Service is the auto-lockout mediator. sessionlogin calls RecordFailure /
// RecordSuccess / TryLazyUnlock; the Service applies the StaleWindow +
// Threshold + LockoutTTL policy, persists counter state via UserRepository,
// routes lock/unlock through authzmutate.Mutator.ApplyInTx, and emits
// event.user.locked.v1 on auto-lock.
//
// Construction: all dependencies are required (fail-fast on nil — matches the
// OUTBOX-SERVICE-01 convention for outbox-bound services).
type Service struct {
	userRepo     ports.UserRepository `gocell:"required" gocellErr:"accountlockout.NewService: UserRepository required"`      //nolint:lll // R2-approved: struct tag for required-dep funnel cannot be split
	authzmutator *authzmutate.Mutator `gocell:"required" gocellErr:"accountlockout.NewService: authzmutate.Mutator required"` //nolint:lll // R2-approved: struct tag for required-dep funnel cannot be split
	emitter      outbox.CellEmitter   `gocell:"required" gocellErr:"accountlockout.NewService: outbox.CellEmitter required"`  //nolint:lll // R2-approved: struct tag for required-dep funnel cannot be split
	clk          clock.Clock          `gocell:"required" gocellErr:"accountlockout.NewService: clock.Clock required"`         //nolint:lll // R2-approved: struct tag for required-dep funnel cannot be split
	logger       *slog.Logger
	metrics      MetricsRecorder
}

// MetricsRecorder is the narrow interface accountlockout uses for
// observability. The runtime/auth/metrics implementation supplies the
// `auth_account_lockout_total{reason}` CounterVec; tests inject a fake.
//
// Reasons emitted: "threshold_locked" (auto-lock triggered) /
// "lazy_unlocked" (TTL expired, user logged in).
type MetricsRecorder interface {
	IncAccountLockout(ctx context.Context, reason string)
}

// noopMetrics is the default no-op MetricsRecorder used when the caller does
// not supply one (e.g. tests). Production wiring passes the real metrics
// adapter from runtime/auth/metrics.
type noopMetrics struct{}

// IncAccountLockout is intentionally empty — noopMetrics discards all metric
// increments. See noopMetrics godoc for when this is wired.
func (noopMetrics) IncAccountLockout(context.Context, string) {
	// Intentional no-op: noopMetrics discards all metric increments.
}

// Option configures the Service at construction time.
type Option func(*Service)

// WithLogger overrides the default slog.Default() logger.
func WithLogger(l *slog.Logger) Option {
	return func(s *Service) {
		if l != nil {
			s.logger = l
		}
	}
}

// WithMetrics injects the metrics recorder. Default is a no-op recorder.
// Typed-nil inputs are not stored (builder-noop semantics): a bare `m != nil`
// would let a typed-nil recorder overwrite the noopMetrics default and panic at
// the first IncAccountLockout call. See runtime-api.md "Option 范式分层".
func WithMetrics(m MetricsRecorder) Option {
	return func(s *Service) {
		if !validation.IsNilInterface(m) {
			s.metrics = m
		}
	}
}

// NewService constructs a Service. All required dependencies must be non-nil;
// missing deps return an errcode.Error at construction time (fail-fast).
func NewService(
	userRepo ports.UserRepository,
	authzmutator *authzmutate.Mutator,
	emitter outbox.CellEmitter,
	clk clock.Clock,
	opts ...Option,
) (*Service, error) {
	s := &Service{
		userRepo:     userRepo,
		authzmutator: authzmutator,
		emitter:      emitter,
		clk:          clk,
		logger:       slog.Default(),
		metrics:      noopMetrics{},
	}
	for _, opt := range opts {
		opt(s)
	}
	if err := s.validateRequired(); err != nil {
		return nil, err
	}
	return s, nil
}

// RecordFailure records a failed login attempt against user (caller has
// already verified user is not nil and is an authenticatable candidate —
// missing-user attempts MUST NOT call this method, because there is no row
// to update). The caller MUST invoke RecordFailure from within an outer
// RunInTx closure; user MUST have been fetched with GetByUsernameForUpdate
// inside the same tx so the row lock holds across the read-decide-write.
//
// Short-circuit: if user.Status() != StatusActive (i.e. already Locked or
// Suspended), RecordFailure is a no-op (returns nil without touching the
// counter or the funnel). The auto-lockout counter is meaningful only for
// candidates that could otherwise authenticate; non-active states are owned
// by other lifecycle paths:
//
//   - StatusLocked: already auto- or admin-locked; additional failures must
//     not re-emit the lock event or grow the counter unboundedly.
//   - StatusSuspended: admin holds the lifecycle. The failure counter must
//     NOT silently escalate Suspended → Locked, because TryLazyUnlock's TTL
//     would then later flip the row back to Active (re-activating an
//     admin-suspended account). PR #585 review P1#2.
//
// Mutation sequence (when status==Active and a lock is triggered):
//  1. user.RegisterFailedLogin(now, StaleWindow, LockoutTTL, Threshold) —
//     mutates in-memory counter + lockedUntil; returns shouldLock.
//  2. userRepo.UpdateLockoutFields(txCtx, user) — persists counter + ts +
//     lockedUntil in the same tx.
//  3. If shouldLock: authzmutator.ApplyInTx(LockUser{}) → SetStatus(Locked) +
//     epoch bump + session/refresh revoke + repo.Update (status & epoch).
//  4. If shouldLock: emit event.user.locked.v1 with ActorID=SystemActorID.
//  5. If shouldLock: metrics.IncAccountLockout(ctx, "threshold_locked").
func (s *Service) RecordFailure(ctx context.Context, txCtx context.Context, user *domain.User) error {
	if user == nil {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"accountlockout.RecordFailure: user must not be nil")
	}
	if user.Status() != domain.StatusActive {
		// Non-Active short-circuit — the auto-lockout counter is meaningful
		// only for Active users. See godoc for the Locked / Suspended
		// rationale (PR #585 review P1#2).
		return nil
	}
	now := s.clk.Now()
	shouldLock := user.RegisterFailedLogin(now, StaleWindow, LockoutTTL, Threshold)
	if err := s.userRepo.UpdateLockoutFields(txCtx, user); err != nil {
		return fmt.Errorf("accountlockout.RecordFailure: update lockout fields: %w", err)
	}
	if !shouldLock {
		return nil
	}
	if err := s.authzmutator.ApplyInTx(ctx, txCtx, user.ID, authzmutate.LockUser{}, now); err != nil {
		return fmt.Errorf("accountlockout.RecordFailure: apply lock: %w", err)
	}
	if err := s.publishLocked(txCtx, user.ID); err != nil {
		return fmt.Errorf("accountlockout.RecordFailure: emit locked event: %w", err)
	}
	s.metrics.IncAccountLockout(ctx, "threshold_locked")
	s.logger.Warn("account auto-locked",
		slog.String("user_id", user.ID),
		slog.Int("failed_count", user.FailedLoginCount()),
		slog.String("reason", "threshold_locked"))
	return nil
}

// RecordSuccess clears the user's auto-lockout counter on a successful
// authentication. Called by sessionlogin after a successful bcrypt verify and
// the credentialauthority assert. Caller must invoke from within the login
// tx so the reset co-commits with the session/refresh INSERTs.
//
// No-op when the user is already in the clean state — FailedLoginCount==0
// AND LastFailedAt==nil AND AutoLockoutDeadline()==nil. All three must hold;
// the third condition matters because a TryLazyUnlock that ran in the same
// tx earlier may leave the in-memory state clean while the persisted row
// already has these columns zeroed (the implementation guard mirrors the
// in-memory predicate to avoid a redundant UPDATE on every successful login).
func (s *Service) RecordSuccess(txCtx context.Context, user *domain.User) error {
	if user == nil {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"accountlockout.RecordSuccess: user must not be nil")
	}
	if user.FailedLoginCount() == 0 && user.LastFailedAt() == nil && user.AutoLockoutDeadline() == nil {
		return nil
	}
	user.ResetFailedLogins()
	if err := s.userRepo.UpdateLockoutFields(txCtx, user); err != nil {
		return fmt.Errorf("accountlockout.RecordSuccess: update lockout fields: %w", err)
	}
	return nil
}

// TryLazyUnlock checks whether user is auto-locked with an expired TTL and,
// if so, transparently unlocks the account in-tx (status=Active + counter=0
// + lockedUntil=nil). Returns (unlocked=true, nil) when an unlock occurred,
// (false, nil) when no action was taken (not locked / TTL not elapsed /
// manually locked with no TTL), or (false, err) on a downstream error.
//
// Manual admin lock (locked_until=nil) is never lazy-unlocked: only auto-
// locks set the TTL, so a nil locked_until signals "human-initiated, requires
// human unlock". The break-glass path for human-locked accounts is admin
// unlock via identitymanage.Unlock.
//
// Caller (sessionlogin) invokes TryLazyUnlock inside its login tx immediately
// after GetByUsernameForUpdate; on unlocked==true, caller refreshes its view
// of user by calling GetByIDForUpdate again to pick up the post-mutation
// status/epoch (or uses the mutated in-memory user — ActivateUser mutation
// updates the same aggregate via repo.GetByIDForUpdate in ApplyInTx).
func (s *Service) TryLazyUnlock(ctx context.Context, txCtx context.Context, user *domain.User) (bool, error) {
	if user == nil {
		return false, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"accountlockout.TryLazyUnlock: user must not be nil")
	}
	if user.Status() != domain.StatusLocked {
		return false, nil
	}
	until := user.AutoLockoutDeadline()
	if until == nil {
		// Manual lock — no TTL, no lazy unlock.
		return false, nil
	}
	now := s.clk.Now()
	if now.Before(*until) {
		// Still within lockout window.
		return false, nil
	}
	// TTL elapsed: apply ActivateUser mutation (which also calls
	// user.ResetFailedLogins() via the mutation.apply extension).
	if err := s.authzmutator.ApplyInTx(ctx, txCtx, user.ID, authzmutate.ActivateUser{}, now); err != nil {
		return false, fmt.Errorf("accountlockout.TryLazyUnlock: apply activate: %w", err)
	}
	if err := s.publishUnlocked(txCtx, user.ID); err != nil {
		return false, fmt.Errorf("accountlockout.TryLazyUnlock: emit unlocked event: %w", err)
	}
	s.metrics.IncAccountLockout(ctx, "lazy_unlocked")
	s.logger.Info("account lazy-unlocked",
		slog.String("user_id", user.ID),
		slog.Time("locked_until", *until),
		slog.String("reason", "lazy_unlocked"))
	return true, nil
}

// publishLocked emits event.user.locked.v1 with ActorID=SystemActorID into the
// outbox inside the caller's tx (txCtx). Wire shape mirrors
// identitymanage.lockUserAndRevokeSessions: same topic, same DTO, same JSON
// schema — consumers cannot distinguish an admin-initiated lock from an
// auto-lock by structure, only by the ActorID value.
func (s *Service) publishLocked(txCtx context.Context, userID string) error {
	payload, err := json.Marshal(dto.UserLockedEvent{UserID: userID, ActorID: SystemActorID})
	if err != nil {
		return fmt.Errorf("accountlockout: marshal locked event: %w", err)
	}
	entry := outbox.Entry{
		ID:        outbox.MustNewEntryID(),
		EventType: dto.TopicUserLocked,
		Payload:   payload,
	}
	if err := s.emitter.Emit(txCtx, entry); err != nil {
		return fmt.Errorf("accountlockout: emit locked event: %w", err)
	}
	return nil
}

// publishUnlocked emits event.user.unlocked.v1 with ActorID=SystemActorID into
// the outbox inside the caller's tx (txCtx). Mirrors publishLocked: same DTO
// shape, same SystemActorID sentinel — consumers can distinguish auto-unlock
// from admin-initiated unlock only by ActorID value.
func (s *Service) publishUnlocked(txCtx context.Context, userID string) error {
	payload, err := json.Marshal(dto.UserUnlockedEvent{UserID: userID, ActorID: SystemActorID})
	if err != nil {
		return fmt.Errorf("accountlockout: marshal unlocked event: %w", err)
	}
	entry := outbox.Entry{
		ID:        outbox.MustNewEntryID(),
		EventType: dto.TopicUserUnlocked,
		Payload:   payload,
	}
	if err := s.emitter.Emit(txCtx, entry); err != nil {
		return fmt.Errorf("accountlockout: emit unlocked event: %w", err)
	}
	return nil
}
