package app

import "context"

// verifyCodegenContract runs in an ephemeral worktree
// (codegen.VerifyInWorktree) with no cancelable downstream; ctx is part of
// the uniform verifySubcommands handler signature.
func verifyCodegenContract(_ context.Context, args []string) error {
	return runCodegenVerify(generateContractSpec, args)
}
