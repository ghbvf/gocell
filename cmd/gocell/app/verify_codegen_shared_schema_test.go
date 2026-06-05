package app

import (
	"context"
	"testing"
)

// TestVerifyCodegenSharedSchema_HappyPath verifies that the real worktree repo
// is in a "mirror in-sync" state: all copies of error-response-v1.schema.json
// declared in sharedschema.Mirrors match the canonical source file.
//
// This test calls verifyCodegenSharedSchema directly; it relies on findRoot()
// (via os.Getwd()) resolving to the actual module root, where the mirrors are
// already committed and up-to-date.
//
// NOTE: this test is NOT parallel because findRoot() uses os.Getwd(), which is
// process-global. Running it in parallel with tests that call chdirToRoot()
// would introduce a data race.
//
// Drift / tamper path tests are intentionally NOT duplicated here because
// sharedschema.Verify is a pure function tested directly in the
// tools/codegen/sharedschema package.
func TestVerifyCodegenSharedSchema_HappyPath(t *testing.T) {
	// Not parallel: uses os.Getwd via findRoot() which is process-global.
	// chdir to the real repo root: post-#1557 split findRoot() from the cmd/gocell
	// module dir resolves cmd/gocell/, where the shared-schema mirrors do not live.
	chdirToRoot(t, repoRoot(t))
	if err := verifyCodegenSharedSchema(context.Background(), nil); err != nil {
		t.Fatalf("verifyCodegenSharedSchema on in-sync repo: %v", err)
	}
}

// TestVerifyCodegenSharedSchema_Drift exercises the drift branch: a temp repo
// with the canonical source but no mirrors leaves every declared mirror missing,
// so Verify reports drift and the command returns an error (stderr drift loop +
// fix hint + non-zero return).
//
// Not parallel: makeMinimalProject uses os.Chdir (process-global).
func TestVerifyCodegenSharedSchema_Drift(t *testing.T) {
	root := makeMinimalProject(t)
	writeSharedSchemaCanonical(t, root)
	if err := verifyCodegenSharedSchema(context.Background(), nil); err == nil {
		t.Fatal("verifyCodegenSharedSchema with missing mirrors: expected drift error, got nil")
	}
}

// TestVerifyCodegenSharedSchema_CanonicalReadError asserts that a repo missing
// the canonical source makes sharedschema.Verify fail and the error propagates.
//
// Not parallel: makeMinimalProject uses os.Chdir (process-global).
func TestVerifyCodegenSharedSchema_CanonicalReadError(t *testing.T) {
	makeMinimalProject(t) // go.mod only, no canonical schema
	if err := verifyCodegenSharedSchema(context.Background(), nil); err == nil {
		t.Fatal("verifyCodegenSharedSchema without canonical: expected error, got nil")
	}
}
