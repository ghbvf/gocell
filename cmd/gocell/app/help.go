package app

import (
	"fmt"
	"strings"
)

// isHelpFlag reports whether arg requests sub-command help.
//
// dispatch.go advertises `gocell <command> -h` as the discovery path; without
// this gate runGenerate/runVerify/runScaffold/runCheck would treat -h as an
// unknown sub-type because they parse args[0] before delegating to flag.Parse.
func isHelpFlag(arg string) bool {
	return arg == "-h" || arg == "--help" || arg == "help"
}

// helpEntry is one type listed under a sub-command's "Types:" section. desc
// can span multiple lines; printHelp indents continuation lines under the
// type name so the help surface stays aligned.
type helpEntry struct {
	name string
	desc []string
}

// printHelp renders a uniform help surface for sub-commands. The shape is
//
//	Usage: gocell <verb> <type> [flags]
//
//	Types:
//	  <name>   <desc[0]>
//	           <desc[1]>
//	           ...
//
//	<footer>
//
// Adding a new type to a sub-command means appending a helpEntry; missing
// the help line is impossible because the data structure is the source of
// truth for the help renderer.
func printHelp(verb string, entries []helpEntry, footer ...string) {
	fmt.Printf("Usage: gocell %s <type> [flags]\n", verb)
	fmt.Println()
	fmt.Println("Types:")
	width := longestEntryName(entries)
	for _, e := range entries {
		first := true
		for _, line := range e.desc {
			if first {
				fmt.Printf("  %-*s  %s\n", width, e.name, line)
				first = false
				continue
			}
			fmt.Printf("  %-*s  %s\n", width, "", line)
		}
		if first {
			// entry with no description; still emit the name so the type
			// is discoverable.
			fmt.Printf("  %s\n", e.name)
		}
	}
	if len(footer) == 0 {
		return
	}
	fmt.Println()
	fmt.Println(strings.Join(footer, "\n"))
}

func longestEntryName(entries []helpEntry) int {
	max := 0
	for _, e := range entries {
		if n := len(e.name); n > max {
			max = n
		}
	}
	return max
}

// printGenerateHelp documents the generate sub-tree, including the ownership
// boundary between gocell-generated and hand-written files. The boundary is
// enforced by writeGeneratedFile, but operators only see the enforcement as a
// terse "refusing to overwrite non-generated file" error; this surface tells
// them what they need to know up front.
func printGenerateHelp() error {
	printHelp("generate", []helpEntry{
		{"assembly", []string{
			"Generate the assembly entrypoint cmd/<id>/main.go,",
			"assemblies/<id>/generated/boundary.yaml, and",
			"cmd/<id>/modules_gen.go (the cell→Module factory).",
			"Generated files are owned by gocell. Hand-written",
			"helpers may live in cmd/<id>/run.go etc., but",
			"cmd/<id>/main.go and cmd/<id>/modules_gen.go must",
			"carry the gocell generated header or generation",
			"aborts to protect your edits.",
			"--id=<assemblyID> | --all [--module=<module>]",
		}},
		{"metrics-schema", []string{
			"Generate assemblies/<id>/generated/metrics-schema.yaml",
			"by walking the assembly's reachable packages with",
			"go/types. --id=<assemblyID>",
		}},
		{"cell", []string{
			"Render cell_gen.go and slice_gen.go from cell.yaml /",
			"slice.yaml. Default: all opted-in cells (goStructName set).",
			"Optional: [<cellID>] scopes to a single cell.",
			"--verify reports drift without writing; --dry-run prints",
			"would-write file paths without writing.",
			"CI: commit cell_gen.go and run with --verify to detect stale artifacts.",
		}},
		{"contract", []string{
			"Render generated/contracts/**/*_gen.go from contract.yaml",
			"+ JSON schemas. <contractID> | --all [--dry-run | --verify].",
			"--verify reports drift without writing; --dry-run prints",
			"would-write paths without writing.",
			"Prerequisite: set codegen: true in the contract.yaml.",
			"CI: commit *_gen.go files and run with --verify.",
		}},
		{"indexes", []string{"(not implemented)"}},
	},
		"Generated artifacts must be committed in HEAD; gocell verify generated",
		"rejects stale or staged-only files.",
	)
	return nil
}

// printVerifyHelp mirrors printGenerateHelp for the verify sub-tree.
func printVerifyHelp() error {
	printHelp("verify", []helpEntry{
		{"slice", []string{
			"Run verify.unit + verify.contract for a slice.",
			"--id=<cellID/sliceID> [--format text|json|sarif]",
		}},
		{"cell", []string{
			"Run verify.smoke + per-slice checks for a cell.",
			"--id=<cellID> [--format text|json|sarif]",
		}},
		{"journey", []string{
			"Run a single journey or every active journey.",
			"--id=<journeyID> | --active [--format text|json|sarif]",
		}},
		{"targets", []string{
			"List slices/cells/contracts/journeys reachable from",
			"the given files. --files=<file1,file2,...>",
		}},
		{"generated", []string{
			"Verify assembly entrypoints, boundary.yaml, and",
			"metrics-schema.yaml against metadata-derived",
			"expectations and HEAD. Fails on stale, staged-only,",
			"or unexpected committed artifacts. [--module=<module>]",
		}},
		{"codegen-cell", []string{
			"Verify cell_gen.go / slice_gen.go are in sync with",
			"cell.yaml / slice.yaml. Default: --local in-place verify",
			"(fast, no sandbox). CI: pass --local=false to use the",
			"K8s-style git worktree sandbox mode.",
		}},
		{"codegen-contract", []string{
			"Verify generated/contracts/**/*_gen.go are in sync with",
			"contract.yaml / schema files. Default: --local in-place",
			"verify (fast, no sandbox). CI: pass --local=false for",
			"git worktree sandbox mode.",
		}},
		{"codegen-assembly", []string{
			"Verify cmd/*/modules_gen.go are in sync with assembly.yaml /",
			"cell.yaml goStructName. Default --local in-place verify (fast).",
			"CI: pass --local=false for git worktree sandbox.",
		}},
	})
	return nil
}

// printScaffoldHelp documents scaffold sub-types.
func printScaffoldHelp() error {
	printHelp("scaffold", []helpEntry{
		{"cell", []string{
			"Create cell skeleton + example slice + example contract.",
			"--id=<id> --team=<team> --role=<role>",
			"[--type=core|edge|support] [--level=L0..L4]",
			"[--with-http] [--with-events] [--with-both]",
			"[--skip-generate] [--dry-run]",
			"Note: --id must not contain '-' (use nodash identifiers).",
		}},
		{"slice", []string{
			"Create cells/<cellID>/slices/<id>/slice.yaml.",
			"--id=<id> --cell=<cellID> [--dry-run]",
			"Note: --id must not contain '-' (use nodash identifiers).",
		}},
		{"contract", []string{
			"Create contracts/<kind>/<domain>/<v>/contract.yaml.",
			"--id=<id> --kind=<kind> --owner=<cellID> [--dry-run]",
		}},
		{"journey", []string{
			"Create journeys/<id>.yaml.",
			"--id=<id> --goal=<goal> --team=<team> --cells=<a,b,...>",
			"[--dry-run]",
		}},
		{"assembly", []string{
			"Create assemblies/<id>/assembly.yaml + cmd/<id>/run.go + app.go.",
			"--id=<id> --cells=<a,b,...> --team=<team> --role=<role>",
			"[--deploy=k8s|compose|binary]（默认 k8s） [--skip-generate] [--dry-run]",
		}},
	},
		"--dry-run validates inputs and path conflicts without writing.",
	)
	return nil
}

// printCheckHelp documents check sub-types.
func printCheckHelp() error {
	printHelp("check", []helpEntry{
		{"contract-health", []string{
			"Aggregate contract metadata health.",
			"[--format text|json|sarif]",
		}},
		{"slice-coverage", []string{
			"Slice coverage of a cell.",
			"--cell=<cellID>",
		}},
		{"assembly-completeness", []string{
			"Assembly cell-set vs declared boundary.",
			"--id=<assemblyID>",
		}},
		{"journey-readiness", []string{
			"Journey readiness against status-board.",
			"--journey=<journeyID>",
		}},
		{"l0-imports", []string{
			"L0 dependency direction.",
			"--cell=<cellID>",
		}},
		{"unconditional-skip", []string{
			"Static analysis for unconditional t.Skip.",
			"[--format text|json|sarif]",
		}},
	})
	return nil
}
