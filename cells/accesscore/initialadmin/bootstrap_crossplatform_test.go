//go:build unix || windows

package initialadmin

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"

	"github.com/ghbvf/gocell/cells/accesscore/internal/mem"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestBootstrapper_FullFlow_OnCurrentOS exercises the complete bootstrap flow
// (user created, role assigned, credfile written, cleaner returned) on the
// current operating system. It uses in-memory repos, a low-cost bcrypt hasher,
// and a temp dir for the credential file so no root / system-directory access
// is required.
func TestBootstrapper_FullFlow_OnCurrentOS(t *testing.T) {
	handler := &capturingHandlerCross{}
	logger := slog.New(handler)
	userRepo := mem.NewUserRepository()
	roleRepo := mem.NewRoleRepository()
	sched := newFakeSchedulerCross()
	credPath := filepath.Join(t.TempDir(), "initial_admin_password")

	deps := BootstrapDeps{
		UserRepo: userRepo,
		RoleRepo: roleRepo,
		Logger:   logger,
	}

	cfg := bootstrapConfig{
		CredentialPath: credPath,
		TTL:            time.Hour,
		PasswordSource: newFixedPasswordSourceCross(),
		Scheduler:      sched,
		Hasher:         BcryptHasher{Cost: bcrypt.MinCost},
	}

	bs, err := newBootstrapper(deps, cfg)
	require.NoError(t, err)

	result, err := bs.ensureAdmin(context.Background())
	require.NoError(t, err)
	cleaner := result.Cleaner
	assert.NotNil(t, cleaner, "expected non-nil cleaner on first bootstrap")

	// Credential file must exist.
	_, statErr := os.Stat(credPath)
	require.NoError(t, statErr, "credential file must be created")

	// File must contain the expected fields.
	contents, readErr := os.ReadFile(filepath.Clean(credPath))
	require.NoError(t, readErr)
	content := string(contents)
	assert.True(t, strings.Contains(content, "username=admin"), "credfile must contain username")
	assert.True(t, strings.Contains(content, "password="), "credfile must contain password")
	assert.True(t, strings.Contains(content, "expires_at="), "credfile must contain expires_at")

	// expires_at must be within TTL (1h ± 5s).
	expiresAt, parseErr := readCredentialExpiresAt(credPath)
	require.NoError(t, parseErr)
	remaining := time.Until(expiresAt)
	assert.True(t, remaining > 0, "expires_at must be in the future")
	assert.True(t, remaining <= time.Hour+5*time.Second, "expires_at must be within TTL")

	// Admin user must exist with PasswordResetRequired=true.
	user, getErr := userRepo.GetByUsername(context.Background(), "admin")
	require.NoError(t, getErr)
	_, idParseErr := uuid.Parse(user.ID)
	assert.NoError(t, idParseErr, "user ID must be a valid UUID")
	assert.True(t, user.PasswordResetRequired, "PasswordResetRequired must be set")

	// Admin role must be assigned.
	roles, rolesErr := roleRepo.GetByUserID(context.Background(), user.ID)
	require.NoError(t, rolesErr)
	require.Len(t, roles, 1)
}

// fixedReaderCross is an io.Reader that produces a deterministic sequence.
type fixedReaderCross struct{ data []byte }

func (r *fixedReaderCross) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = r.data[i%len(r.data)]
	}
	return len(p), nil
}

// newFixedPasswordSourceCross returns a deterministic entropy source for tests.
func newFixedPasswordSourceCross() *fixedReaderCross {
	b := make([]byte, 32)
	for i := range b {
		b[i] = byte('A' + (i % 26))
	}
	return &fixedReaderCross{data: b}
}
