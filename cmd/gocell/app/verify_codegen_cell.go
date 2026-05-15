package app

import "context"

// verifyCodegenCell runs in an ephemeral worktree (codegen.VerifyInWorktree)
// with no cancelable downstream; ctx is part of the uniform
// verifySubcommands handler signature.
func verifyCodegenCell(_ context.Context, args []string) error {
	return runCodegenVerify(generateCellSpec, args)
}
