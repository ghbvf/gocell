package app

import "fmt"

// isHelpFlag reports whether arg requests sub-command help.
//
// dispatch.go advertises `gocell <command> -h` as the discovery path; without
// this gate runGenerate/runVerify/runScaffold/runCheck would treat -h as an
// unknown sub-type because they parse args[0] before delegating to flag.Parse.
func isHelpFlag(arg string) bool {
	return arg == "-h" || arg == "--help" || arg == "help"
}

// printGenerateHelp documents the generate sub-tree, including the ownership
// boundary between gocell-generated and hand-written files. The boundary is
// enforced by writeGeneratedFile, but operators only see the enforcement as a
// terse "refusing to overwrite non-generated file" error; this surface tells
// them what they need to know up front.
func printGenerateHelp() error {
	fmt.Println("Usage: gocell generate <type> [flags]")
	fmt.Println()
	fmt.Println("Types:")
	fmt.Println("  assembly        Generate the assembly entrypoint (cmd/<id>/main.go)")
	fmt.Println("                  and assemblies/<id>/generated/boundary.yaml.")
	fmt.Println("                  Generated files are owned by gocell. Hand-written")
	fmt.Println("                  helpers may live in cmd/<id>/run.go etc., but")
	fmt.Println("                  cmd/<id>/main.go must carry the gocell generated")
	fmt.Println("                  header or generation aborts to protect your edits.")
	fmt.Println("                  --id=<assemblyID> [--module=<module>]")
	fmt.Println("  metrics-schema  Generate assemblies/<id>/generated/metrics-schema.yaml")
	fmt.Println("                  by walking the assembly's reachable packages with")
	fmt.Println("                  go/types. --id=<assemblyID>")
	fmt.Println("  indexes         (not implemented)")
	fmt.Println()
	fmt.Println("Generated artifacts must be committed in HEAD; gocell verify generated")
	fmt.Println("rejects stale or staged-only files.")
	return nil
}

// printVerifyHelp mirrors printGenerateHelp for the verify sub-tree.
func printVerifyHelp() error {
	fmt.Println("Usage: gocell verify <type> [flags]")
	fmt.Println()
	fmt.Println("Types:")
	fmt.Println("  slice      Run verify.unit + verify.contract for a slice.")
	fmt.Println("             --id=<cellID/sliceID> [--format text|json|sarif]")
	fmt.Println("  cell       Run verify.smoke + per-slice checks for a cell.")
	fmt.Println("             --id=<cellID> [--format text|json|sarif]")
	fmt.Println("  journey    Run a single journey or every active journey.")
	fmt.Println("             --id=<journeyID> | --active [--format text|json|sarif]")
	fmt.Println("  targets    List slices/cells/contracts/journeys reachable from")
	fmt.Println("             the given files. --files=<file1,file2,...>")
	fmt.Println("  generated  Verify assembly entrypoints, boundary.yaml, and")
	fmt.Println("             metrics-schema.yaml against metadata-derived")
	fmt.Println("             expectations and HEAD. Fails on stale, staged-only,")
	fmt.Println("             or unexpected committed artifacts. [--module=<module>]")
	return nil
}

// printScaffoldHelp documents scaffold sub-types.
func printScaffoldHelp() error {
	fmt.Println("Usage: gocell scaffold <type> [flags]")
	fmt.Println()
	fmt.Println("Types:")
	fmt.Println("  cell      Create cells/<id>/cell.yaml. --id=<id> --team=<team>")
	fmt.Println("            [--type=core|edge|support] [--level=L0..L4] [--dry-run]")
	fmt.Println("  slice     Create cells/<cellID>/slices/<id>/slice.yaml.")
	fmt.Println("            --id=<id> --cell=<cellID> [--dry-run]")
	fmt.Println("  contract  Create contracts/<kind>/<domain>/<v>/contract.yaml.")
	fmt.Println("            --id=<id> --kind=<kind> --owner=<cellID> [--dry-run]")
	fmt.Println("  journey   Create journeys/<id>.yaml.")
	fmt.Println("            --id=<id> --goal=<goal> --team=<team> --cells=<a,b,...>")
	fmt.Println("            [--dry-run]")
	fmt.Println()
	fmt.Println("--dry-run validates inputs and path conflicts without writing.")
	return nil
}

// printCheckHelp documents check sub-types.
func printCheckHelp() error {
	fmt.Println("Usage: gocell check <type> [flags]")
	fmt.Println()
	fmt.Println("Types:")
	fmt.Println("  contract-health         Aggregate contract metadata health.")
	fmt.Println("                          [--format text|json|sarif]")
	fmt.Println("  slice-coverage          Slice coverage of a cell.")
	fmt.Println("                          --cell=<cellID>")
	fmt.Println("  assembly-completeness   Assembly cell-set vs declared boundary.")
	fmt.Println("                          --id=<assemblyID>")
	fmt.Println("  journey-readiness       Journey readiness against status-board.")
	fmt.Println("                          --journey=<journeyID>")
	fmt.Println("  l0-imports              L0 dependency direction.")
	fmt.Println("                          --cell=<cellID>")
	fmt.Println("  unconditional-skip      Static analysis for unconditional t.Skip.")
	fmt.Println("                          [--format text|json|sarif]")
	return nil
}
