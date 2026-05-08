package scanner_test

import (
	"go/ast"
	"go/token"
	"testing"

	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
)

func TestStringLitValue_DoubleQuoted(t *testing.T) {
	lit := &ast.BasicLit{Kind: token.STRING, Value: `"admin"`}
	got, ok := scanner.StringLitValue(lit)
	if !ok || got != "admin" {
		t.Errorf("double-quoted: got (%q, %v), want (\"admin\", true)", got, ok)
	}
}

func TestStringLitValue_RawString(t *testing.T) {
	lit := &ast.BasicLit{Kind: token.STRING, Value: "`admin`"}
	got, ok := scanner.StringLitValue(lit)
	if !ok || got != "admin" {
		t.Errorf("raw string: got (%q, %v), want (\"admin\", true)", got, ok)
	}
}

func TestStringLitValue_RuneLiteralRejected(t *testing.T) {
	lit := &ast.BasicLit{Kind: token.CHAR, Value: `'a'`}
	got, ok := scanner.StringLitValue(lit)
	if ok || got != "" {
		t.Errorf("rune literal: got (%q, %v), want (\"\", false)", got, ok)
	}
}

func TestStringLitValue_EmptyString(t *testing.T) {
	lit := &ast.BasicLit{Kind: token.STRING, Value: `""`}
	got, ok := scanner.StringLitValue(lit)
	if !ok || got != "" {
		t.Errorf("empty string: got (%q, %v), want (\"\", true)", got, ok)
	}
}

func TestStringLitValue_IntLiteralRejected(t *testing.T) {
	lit := &ast.BasicLit{Kind: token.INT, Value: `42`}
	got, ok := scanner.StringLitValue(lit)
	if ok || got != "" {
		t.Errorf("int literal: got (%q, %v), want (\"\", false)", got, ok)
	}
}

func TestStringLitValue_HexEscape(t *testing.T) {
	lit := &ast.BasicLit{Kind: token.STRING, Value: `"\x61dmin"`}
	got, ok := scanner.StringLitValue(lit)
	if !ok || got != "admin" {
		t.Errorf("hex escape: got (%q, %v), want (\"admin\", true)", got, ok)
	}
}

func TestStringLitValue_MalformedRejected(t *testing.T) {
	lit := &ast.BasicLit{Kind: token.STRING, Value: `"unterminated`}
	got, ok := scanner.StringLitValue(lit)
	if ok || got != "" {
		t.Errorf("malformed: got (%q, %v), want (\"\", false)", got, ok)
	}
}

func TestStringLitValue_NilLit(t *testing.T) {
	got, ok := scanner.StringLitValue(nil)
	if ok || got != "" {
		t.Errorf("nil lit: got (%q, %v), want (\"\", false)", got, ok)
	}
}
