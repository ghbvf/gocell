//go:build unix || windows

package initialadmin

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/ghbvf/gocell/cells/accesscore/internal/adminprovision"
	"github.com/ghbvf/gocell/cells/accesscore/internal/domain"
	"github.com/ghbvf/gocell/pkg/errcode"
)

// envDrivenBootstrapper provisions the initial admin user from injected
// env credentials — no random password generation, no credential file.
//
// ref: minio/minio internal/auth/credentials.go (startup length fail-fast)
type envDrivenBootstrapper struct {
	deps        BootstrapDeps
	hasher      PasswordHasher
	provisioner *adminprovision.Provisioner
}

// newEnvDrivenBootstrapper validates deps and returns a ready bootstrapper.
func newEnvDrivenBootstrapper(deps BootstrapDeps, hasher PasswordHasher) (*envDrivenBootstrapper, error) {
	if deps.UserRepo == nil {
		return nil, errcode.New(errcode.KindInternal, errcode.ErrCellInvalidConfig,
			"initialadmin: envDrivenBootstrapper requires UserRepo")
	}
	if deps.RoleRepo == nil {
		return nil, errcode.New(errcode.KindInternal, errcode.ErrCellInvalidConfig,
			"initialadmin: envDrivenBootstrapper requires RoleRepo")
	}
	if deps.Logger == nil {
		return nil, errcode.New(errcode.KindInternal, errcode.ErrCellInvalidConfig,
			"initialadmin: envDrivenBootstrapper requires Logger")
	}
	if hasher == nil {
		hasher = defaultPasswordHasher()
	}
	prov, err := adminprovision.NewProvisioner(deps.UserRepo, deps.RoleRepo, deps.Logger, uuid.NewString, deps.Clock)
	if err != nil {
		return nil, fmt.Errorf("initialadmin: env-driven: build provisioner: %w", err)
	}
	return &envDrivenBootstrapper{deps: deps, hasher: hasher, provisioner: prov}, nil
}

// ensureAdminFromCreds provisions the admin using the supplied credentials.
// Returns created==true exactly when this call wrote the admin row; an
// already-existing admin returns created==false with no error.
//
// The created bool is enough for the lifecycle to decide whether to log
// "initial admin created" — the persistent startup credential model (ADR §D9)
// does not warn on already-exists, since the env credentials remain mandatory
// for setup endpoint protection regardless of admin presence.
func (b *envDrivenBootstrapper) ensureAdminFromCreds(ctx context.Context, creds BootstrapCredentials) (created bool, err error) {
	// Fast-path: if admin already exists, no work needed.
	exists, err := b.provisioner.Status(ctx)
	if err != nil {
		return false, fmt.Errorf("initialadmin: env-driven status: %w", err)
	}
	if exists {
		return false, nil
	}

	// Hash the injected password. The plaintext byte slice is zeroed after hashing.
	passwordBytes := make([]byte, len(creds.Password))
	copy(passwordBytes, creds.Password)
	hash, err := b.hasher.Hash(passwordBytes)
	for i := range passwordBytes {
		passwordBytes[i] = 0
	}
	if err != nil {
		return false, fmt.Errorf("initialadmin: env-driven hash password: %w", err)
	}

	username := string(creds.Username)
	result, err := b.provisioner.Ensure(ctx, adminprovision.ProvisionInput{
		Username:     username,
		Email:        username + "@gocell.local",
		PasswordHash: hash,
		RequireReset: true,
		Source:       domain.UserSourceBootstrap,
	})
	if err != nil {
		return false, fmt.Errorf("initialadmin: env-driven ensure: %w", err)
	}
	switch result.Outcome {
	case adminprovision.OutcomeAlreadyExists, adminprovision.OutcomeRaceSkipped:
		return false, nil
	case adminprovision.OutcomeCreated, adminprovision.OutcomeOrphanRecovered:
		return true, nil
	default:
		return false, fmt.Errorf("initialadmin: env-driven: unexpected provision outcome %d", result.Outcome)
	}
}
