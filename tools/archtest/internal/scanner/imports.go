package scanner

import "go/ast"

// PackageAliases returns the set of local package names by which f imports
// importPath. The default name (the package's declared identifier when no
// rename is given) is returned as "<base>" where <base> is the last path
// segment of importPath; explicit aliases (`import x "..."`) are honored;
// dot-imports (`.`) and blank imports (`_`) are excluded because neither
// produces an AST `<name>.Func` selector call expression that an
// import-aware rule can match.
//
// Example: for importPath = "github.com/ghbvf/gocell/runtime/auth" and a file
// containing both `import "github.com/ghbvf/gocell/runtime/auth"` and
// `import authpkg "github.com/ghbvf/gocell/runtime/auth"`, the returned set
// contains {"auth", "authpkg"}.
//
// A file that does not import importPath returns an empty (non-nil) set.
//
// ref: golang.org/x/tools/go/ast/inspector — file.Imports iteration; honoring
// imp.Name == nil (default) vs explicit alias is the canonical pattern.
func PackageAliases(file *ast.File, importPath string) map[string]struct{} {
	out := map[string]struct{}{}
	if file == nil || importPath == "" {
		return out
	}
	target := `"` + importPath + `"`
	for _, imp := range file.Imports {
		if imp == nil || imp.Path == nil || imp.Path.Value != target {
			continue
		}
		name := defaultPackageName(importPath)
		if imp.Name != nil {
			if imp.Name.Name == "_" || imp.Name.Name == "." {
				continue
			}
			name = imp.Name.Name
		}
		out[name] = struct{}{}
	}
	return out
}

// defaultPackageName returns the last "/"-separated segment of importPath.
// We deliberately do not parse the file's package clause: Go's import
// resolution uses the package name declared by the imported file, but for
// the purposes of selector-expression matching the local identifier defaults
// to the path's basename in the vast majority of cases. Renamed imports
// always go through imp.Name and bypass this fallback.
func defaultPackageName(importPath string) string {
	for i := len(importPath) - 1; i >= 0; i-- {
		if importPath[i] == '/' {
			return importPath[i+1:]
		}
	}
	return importPath
}
