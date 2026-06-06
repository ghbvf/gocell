package archtest

// shared_helpers.go — small utility functions used by both non-test rule
// implementations (saga_invariants.go, etc.) and _test.go files.
//
// Functions that are needed in a non-test .go file cannot be defined only in
// a _test.go file (Go never compiles _test.go when the package is imported).
// This file holds the subset of helpers that crossed the boundary.

import (
	"go/ast"
	"go/token"
	"go/types"
	"strings"
)

// itoa converts n to its decimal string representation without importing
// strconv.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// sliceOrNone renders s as "[a, b, c]" or "(none)" when empty.
func sliceOrNone(s []string) string {
	if len(s) == 0 {
		return "(none)"
	}
	return "[" + strings.Join(s, ", ") + "]"
}

// collectFuncBodyRanges returns (Lbrace, Rbrace) token.Pos pairs for every
// FuncDecl whose name is in allowedNames. Used to check whether a node's Pos
// falls inside a sanctioned function body (see posInRanges).
func collectFuncBodyRanges(file *ast.File, allowedNames ...string) []token.Pos {
	allowed := make(map[string]struct{}, len(allowedNames))
	for _, n := range allowedNames {
		allowed[n] = struct{}{}
	}
	var ranges []token.Pos
	EachInSubtree[ast.FuncDecl](file, func(fn *ast.FuncDecl) {
		if fn.Body == nil || fn.Name == nil {
			return
		}
		if _, ok := allowed[fn.Name.Name]; !ok {
			return
		}
		ranges = append(ranges, fn.Body.Lbrace, fn.Body.Rbrace)
	})
	return ranges
}

// posInRanges reports whether p falls within any (Lbrace, Rbrace) pair in
// ranges (as produced by collectFuncBodyRanges).
func posInRanges(p token.Pos, ranges []token.Pos) bool {
	for i := 0; i+1 < len(ranges); i += 2 {
		if p >= ranges[i] && p <= ranges[i+1] {
			return true
		}
	}
	return false
}

// isContextType reports whether t is context.Context.
func isContextType(t types.Type) bool {
	iface, ok := t.Underlying().(*types.Interface)
	if !ok {
		return false
	}
	named, ok := t.(*types.Named)
	if !ok {
		return false
	}
	_ = iface
	return named.Obj() != nil && named.Obj().Name() == "Context" &&
		named.Obj().Pkg() != nil && named.Obj().Pkg().Path() == "context"
}

// isTimeDurationType reports whether t is time.Duration.
func isTimeDurationType(t types.Type) bool {
	named, ok := t.(*types.Named)
	if !ok {
		return false
	}
	return named.Obj() != nil && named.Obj().Name() == "Duration" &&
		named.Obj().Pkg() != nil && named.Obj().Pkg().Path() == "time"
}
