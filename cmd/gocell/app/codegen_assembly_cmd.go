package app

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/ghbvf/gocell/kernel/assembly"
	"github.com/ghbvf/gocell/kernel/metadata"
	"github.com/ghbvf/gocell/tools/codegen"
)

const codegenAssemblyKind = "assembly"

// runVerifyCodegenAssembly implements `gocell verify codegen-assembly`.
// 默认 --local=true（本地 in-place 校验），CI 通过 --local=false 走 sandbox。
//
// 校验范围：每个 assembly 的 cmd/{id}/modules_gen.go 必须与重生成结果 byte-equal。
// main.go 与 boundary.yaml 走各自的 verify 路径（如有），本子命令只锁 modules_gen.go。
func runVerifyCodegenAssembly(args []string) error {
	fs := flag.NewFlagSet("verify codegen-assembly", flag.ContinueOnError)
	local := fs.Bool("local", true,
		"skip git worktree sandbox; verify in-place against current working tree "+
			"(default true; CI should pass --local=false for sandbox mode)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	root, err := findRoot()
	if err != nil {
		return fmt.Errorf("cannot find project root: %w", err)
	}
	if *local {
		return verifyAssemblyModulesGenInPlace(root)
	}
	return verifyAssemblyModulesGenSandbox(root)
}

func verifyAssemblyModulesGenInPlace(root string) error {
	project, err := parseProject(root)
	if err != nil {
		return err
	}
	drifts, err := collectAssemblyModulesGenDrift(root, project)
	if err != nil {
		return err
	}
	if len(drifts) > 0 {
		for _, f := range drifts {
			fmt.Fprintf(os.Stderr, "drift: %s\n", f)
		}
		writeDriftFixHint(codegenAssemblyKind)
		return fmt.Errorf(driftErrorTemplate, len(drifts), codegenAssemblyKind)
	}
	fmt.Println("Generated assembly modules_gen.go OK (--local).")
	return nil
}

func verifyAssemblyModulesGenSandbox(root string) error {
	res, err := codegen.VerifyInWorktree(root, func(workdir string) error {
		project, perr := parseProject(workdir)
		if perr != nil {
			return perr
		}
		return regenerateAssemblyModulesGen(workdir, project)
	})
	if err != nil {
		return fmt.Errorf("verify codegen-assembly sandbox: %w", err)
	}
	if len(res.Drifted) > 0 {
		fmt.Fprintln(os.Stderr, "ERROR: generated assembly modules_gen.go is out of sync with assembly.yaml/cell.yaml")
		for _, f := range res.Drifted {
			fmt.Fprintf(os.Stderr, "  %s\n", f)
		}
		fmt.Fprintln(os.Stderr, res.DiffSummary)
		writeDriftFixHint(codegenAssemblyKind)
		return fmt.Errorf("codegen drift in %d files", len(res.Drifted))
	}
	fmt.Println("Generated assembly modules_gen.go OK.")
	return nil
}

// regenerateAssemblyModulesGen writes all assembly modules_gen.go files to
// workdir; VerifyInWorktree will diff against the original root afterward.
func regenerateAssemblyModulesGen(workdir string, project *metadata.ProjectMeta) error {
	mod, err := readModule(workdir)
	if err != nil {
		return fmt.Errorf("cannot read module from go.mod: %w", err)
	}
	gen := assembly.NewGenerator(project, mod, workdir)
	for asmID, asm := range project.Assemblies {
		if asm == nil {
			continue
		}
		content, gerr := gen.GenerateModulesGen(asmID)
		if gerr != nil {
			return fmt.Errorf("regenerate modules_gen %s: %w", asmID, gerr)
		}
		entrypointRel := asm.Build.Entrypoint
		if entrypointRel == "" {
			entrypointRel = filepath.Join("cmd", asmID, "main.go")
		}
		outPath := filepath.Join(workdir, filepath.Dir(entrypointRel), "modules_gen.go")
		if _, werr := codegen.Write(codegen.WriteOptions{
			Path:     outPath,
			Content:  content,
			RepoRoot: workdir,
		}); werr != nil {
			return fmt.Errorf("write modules_gen %s: %w", asmID, werr)
		}
	}
	return nil
}

// collectAssemblyModulesGenDrift generates modules_gen content in memory and
// diffs against the on-disk file. Returns relative paths of drifted files.
func collectAssemblyModulesGenDrift(root string, project *metadata.ProjectMeta) ([]string, error) {
	mod, err := readModule(root)
	if err != nil {
		return nil, fmt.Errorf("cannot read module from go.mod: %w", err)
	}
	gen := assembly.NewGenerator(project, mod, root)
	var drifts []string
	for asmID, asm := range project.Assemblies {
		if asm == nil {
			continue
		}
		want, gerr := gen.GenerateModulesGen(asmID)
		if gerr != nil {
			return nil, fmt.Errorf("regenerate modules_gen %s: %w", asmID, gerr)
		}
		entrypointRel := asm.Build.Entrypoint
		if entrypointRel == "" {
			entrypointRel = filepath.Join("cmd", asmID, "main.go")
		}
		outPath := filepath.Join(root, filepath.Dir(entrypointRel), "modules_gen.go")
		got, rerr := os.ReadFile(outPath) //nolint:gosec // path constructed from project metadata, not user input
		if rerr != nil || !bytes.Equal(got, want) {
			drifts = append(drifts, filepath.Join(filepath.Dir(entrypointRel), "modules_gen.go"))
		}
	}
	return drifts, nil
}
