package initialadmin

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ghbvf/gocell/cells/accesscore/internal/domain"
	"github.com/ghbvf/gocell/cells/accesscore/internal/mem"
	"golang.org/x/crypto/bcrypt"
)

func newCrossPlatformTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

type crossPlatformFixedReader struct{ data []byte }

func (r *crossPlatformFixedReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = r.data[i%len(r.data)]
	}
	return len(p), nil
}

func TestBootstrapperEnsureAdmin_CrossPlatformFirstRun(t *testing.T) {
	userRepo := mem.NewUserRepository()
	roleRepo := mem.NewRoleRepository()
	credPath := filepath.Join(t.TempDir(), "initial_admin_password")

	bs, err := newBootstrapper(BootstrapDeps{
		UserRepo: userRepo,
		RoleRepo: roleRepo,
		Logger:   newCrossPlatformTestLogger(),
	}, bootstrapConfig{
		CredentialPath: credPath,
		TTL:            time.Hour,
		PasswordSource: &crossPlatformFixedReader{data: []byte("0123456789abcdef")},
		Hasher:         BcryptHasher{Cost: bcrypt.MinCost},
	})
	if err != nil {
		t.Fatalf("newBootstrapper: %v", err)
	}

	cleaner, err := bs.ensureAdmin(context.Background())
	if err != nil {
		t.Fatalf("ensureAdmin: %v", err)
	}
	if cleaner == nil {
		t.Fatal("ensureAdmin returned nil cleaner on first run")
	}

	data, err := os.ReadFile(credPath)
	if err != nil {
		t.Fatalf("read credential file: %v", err)
	}
	if !strings.Contains(string(data), "username=admin") || !strings.Contains(string(data), "password=") {
		t.Fatalf("credential file missing username/password fields:\n%s", data)
	}

	user, err := userRepo.GetByUsername(context.Background(), "admin")
	if err != nil {
		t.Fatalf("GetByUsername(admin): %v", err)
	}
	if !user.PasswordResetRequired {
		t.Fatal("bootstrap user must require password reset")
	}
	roles, err := roleRepo.GetByUserID(context.Background(), user.ID)
	if err != nil {
		t.Fatalf("GetByUserID: %v", err)
	}
	if len(roles) != 1 || roles[0].ID != domain.RoleAdmin {
		t.Fatalf("roles = %#v, want admin role", roles)
	}
}
