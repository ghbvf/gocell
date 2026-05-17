//go:build integration

package s3

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	tcminio "github.com/testcontainers/testcontainers-go/modules/minio"

	"github.com/ghbvf/gocell/tests/testutil"
)

// Package-shared MinIO container for integration tests that do NOT need
// Stop/Start cycles. Tests that need Stop/Start MUST NOT use
// sharedMinIOContainer — they own their own container via
// testutil.StartMinIOContainer + WithMinIOVolume.
//
// Reuses one container across stateless tests, saving roughly one container
// start/stop cycle (~5s on a warm Docker daemon) versus per-test containers.
var (
	sharedCtr     *tcminio.MinioContainer
	sharedCtrErr  error
	sharedCtrOnce sync.Once
)

// sharedMinIOContainer lazy-inits the package-shared MinIO container. The
// integration bucket is created idempotently on first use.
//
// Container lifetime is owned by TestMain (not the first caller's t), so we
// call testutil.RunMinIOContainer (raw primitive without t.Cleanup) rather
// than testutil.StartMinIOContainer.
func sharedMinIOContainer(t *testing.T) *tcminio.MinioContainer {
	t.Helper()
	testutil.RequireDocker(t)
	sharedCtrOnce.Do(func() {
		ctx := context.Background()
		ctr, runErr := testutil.RunMinIOContainer(ctx)
		if ctr != nil {
			sharedCtr = ctr // keep ref for TestMain Terminate even if subsequent steps fail
		}
		if runErr != nil {
			sharedCtrErr = runErr
			return
		}
		connStr, cerr := ctr.ConnectionString(ctx)
		if cerr != nil {
			sharedCtrErr = fmt.Errorf("shared minio connection string: %w", cerr)
			return
		}
		endpoint := buildEndpoint(connStr)
		createBucket(t, ctx, endpoint, ctr.Username, ctr.Password)
	})
	if sharedCtrErr != nil {
		t.Fatalf("shared MinIO container init failed: %v", sharedCtrErr)
	}
	return sharedCtr
}

func TestMain(m *testing.M) {
	code := m.Run()
	if sharedCtr != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		_ = sharedCtr.Terminate(ctx)
		cancel()
	}
	os.Exit(code)
}
