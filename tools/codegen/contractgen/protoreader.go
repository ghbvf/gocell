package contractgen

import (
	"fmt"
	"go/token"
	"os"
	"regexp"
	"strings"
)

// protoTypeInfo is the proto identity resolved from a .proto file for one rpc
// method: the Go import binding (from the file's go_package option) plus the
// request/response message type simple names (from the rpc declaration). The
// .proto is the single source of truth for these — codegen never hand-derives
// them, so the rendered stub's import + signature provably track the proto
// (GRPC-PROTO-REGISTRY-SINGLE-SOURCE-01).
type protoTypeInfo struct {
	// ProtoPackage is the proto `package` declaration, e.g. "device.command.v1".
	ProtoPackage string
	// ImportPath is the go_package import path (the part before ';'),
	// e.g. "github.com/ghbvf/gocell/generated/contracts/grpc/device/command/v1".
	ImportPath string
	// Alias is the go_package import alias (the part after ';'), e.g. "commandv1".
	Alias string
	// RequestType is the rpc request message simple name, e.g. "IssueCommandRequest".
	RequestType string
	// ResponseType is the rpc response message simple name.
	ResponseType string
}

var (
	// protoPackageRE matches `package device.command.v1;`.
	protoPackageRE = regexp.MustCompile(`(?m)^\s*package\s+([A-Za-z][A-Za-z0-9_.]*)\s*;`)
	// goPackageRE matches `option go_package = "<import-path>;<alias>";`.
	goPackageRE = regexp.MustCompile(`(?m)^\s*option\s+go_package\s*=\s*"([^"]*)"\s*;`)
	// rpcLineRE matches `rpc Name(Req) returns (Resp)`, tolerating arbitrary
	// whitespace, the optional `stream` keyword, and package-qualified message
	// names. Groups: 1=method, 2=req stream?, 3=req type, 4=resp stream?, 5=resp type.
	// No `^` anchor (unlike the package/go_package REs): rpc declarations are
	// always nested+indented inside a service block, and block comments are
	// stripped before matching, so a line anchor would only reject valid input.
	rpcLineRE = regexp.MustCompile(
		`rpc\s+([A-Za-z_]\w*)\s*\(\s*(stream\s+)?([.A-Za-z_][.\w]*)\s*\)\s*returns\s*\(\s*(stream\s+)?([.A-Za-z_][.\w]*)\s*\)`)
	// blockCommentRE matches /* ... */ comments across lines (s flag), non-greedy.
	blockCommentRE = regexp.MustCompile(`(?s)/\*.*?\*/`)
)

// readProtoTypeInfo reads protoAbsPath and resolves the proto identity for the
// given rpc method. Comments are stripped first so a commented-out option or rpc
// declaration is never matched.
func readProtoTypeInfo(protoAbsPath, method string) (protoTypeInfo, error) {
	raw, err := os.ReadFile(protoAbsPath) // #nosec G304 — codegen reads a contracts-relative .proto resolved by the builder
	if err != nil {
		return protoTypeInfo{}, fmt.Errorf("read proto %q: %w", protoAbsPath, err)
	}
	text := stripProtoComments(string(raw))

	pkg, err := parseProtoPackage(text)
	if err != nil {
		return protoTypeInfo{}, err
	}
	importPath, alias, err := parseGoPackage(text)
	if err != nil {
		return protoTypeInfo{}, err
	}
	req, resp, err := parseRPCMethod(text, method)
	if err != nil {
		return protoTypeInfo{}, err
	}
	return protoTypeInfo{
		ProtoPackage: pkg,
		ImportPath:   importPath,
		Alias:        alias,
		RequestType:  req,
		ResponseType: resp,
	}, nil
}

// parseProtoPackage extracts the proto `package` declaration.
func parseProtoPackage(text string) (string, error) {
	m := protoPackageRE.FindStringSubmatch(text)
	if m == nil {
		return "", fmt.Errorf("proto: missing `package` declaration")
	}
	return m[1], nil
}

// parseGoPackage extracts the import path + alias from `option go_package`. The
// alias (after ';') is required — protoc-gen-go uses it as the generated Go
// package name, which the stub imports under. Both halves are validated because
// both are injected verbatim into generated Go (the import line + the
// *alias.Type signature): the import path must carry no whitespace (goimports
// would reject it downstream, but with an opaque parse error rather than a
// proto-pointing one), and the alias must be a Go identifier — same guard the
// rpc method already gets in buildGRPCSpec.
func parseGoPackage(text string) (importPath, alias string, err error) {
	m := goPackageRE.FindStringSubmatch(text)
	if m == nil {
		return "", "", fmt.Errorf("proto: missing `option go_package`")
	}
	path, alias, ok := strings.Cut(m[1], ";")
	if !ok || path == "" || alias == "" {
		return "", "", fmt.Errorf(`proto: option go_package %q must be "<import-path>;<alias>"`, m[1])
	}
	if strings.ContainsAny(path, " \t\r\n") {
		return "", "", fmt.Errorf("proto: go_package import path %q must not contain whitespace", path)
	}
	if !token.IsIdentifier(alias) {
		return "", "", fmt.Errorf("proto: go_package alias %q must be a valid Go identifier", alias)
	}
	return path, alias, nil
}

// parseRPCMethod finds the rpc declaration for method and returns its request /
// response message simple names. Streaming rpcs are rejected (unary only until
// PR 10).
func parseRPCMethod(text, method string) (req, resp string, err error) {
	for _, m := range rpcLineRE.FindAllStringSubmatch(text, -1) {
		if m[1] != method {
			continue
		}
		if m[2] != "" || m[4] != "" {
			return "", "", fmt.Errorf("proto: rpc %q is streaming; codegen supports unary only (PR 10)", method)
		}
		return lastDotSegment(m[3]), lastDotSegment(m[5]), nil
	}
	return "", "", fmt.Errorf("proto: rpc method %q not found", method)
}

// stripProtoComments removes /* */ block comments (across lines) and // line
// comments, leaving string literals intact, so the scanners above never match
// commented-out declarations. Block comments are removed first so the per-line
// pass operates on block-comment-free text.
//
// Blind spot (accepted): a "/*" or "//" embedded inside a proto string literal
// would be mis-stripped; likewise an escaped quote (\") inside a string toggles
// indexLineComment's inStr state and may misclassify a following "//". Proto
// option strings (go_package, etc.) do not contain these sequences, and the
// fixture controls the grammar; readProtoTypeInfo only reads contracts-relative
// proto files authored in-repo.
func stripProtoComments(src string) string {
	src = blockCommentRE.ReplaceAllString(src, " ")
	var b strings.Builder
	b.Grow(len(src))
	for _, line := range strings.Split(src, "\n") {
		if i := indexLineComment(line); i >= 0 {
			line = line[:i]
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String()
}

// indexLineComment returns the byte index of the first // that begins a line
// comment (not inside a string literal), or -1.
func indexLineComment(line string) int {
	inStr := false
	for i := 0; i < len(line); i++ {
		switch line[i] {
		case '"':
			inStr = !inStr
		case '/':
			if !inStr && i+1 < len(line) && line[i+1] == '/' {
				return i
			}
		}
	}
	return -1
}

// lastDotSegment strips a leading package qualifier from a proto message
// reference (`.pkg.Msg` → `Msg`), returning the simple type name.
func lastDotSegment(s string) string {
	if i := strings.LastIndex(s, "."); i >= 0 {
		return s[i+1:]
	}
	return s
}
