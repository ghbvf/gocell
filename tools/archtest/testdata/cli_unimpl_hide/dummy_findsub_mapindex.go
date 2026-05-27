// Fixture for the upstream form-uniqueness reverse self-check
// (CLI-UNIMPL-HIDE-01 / CLI-TOPLEVEL-HELP-REGISTRY-01).
//
// runGenerate calls findSub(generateSubcommands, …) — so the weaker
// "findSub appears somewhere" check would pass — but discards the resolved
// handler and dispatches via a name→handler map index instead. The
// form-unique scanDispatchViaRegistry MUST flag it: the findSub result is
// never invoked. Not compiled (testdata).
package fixture

func runGenerate(ctx context.Context, args []string) error {
	cmd, ok := findSub(generateSubcommands, args[0]) // dummy: result discarded
	_ = ok
	_ = cmd
	h := handlerMap[args[0]] // real dispatch via map index, bypassing findSub
	return h(ctx, args[1:])
}
