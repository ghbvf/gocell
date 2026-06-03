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
// rpc method within the contract's declared service (fully-qualified, e.g.
// "device.command.v1.DeviceCommandService"). Comments are stripped first so a
// commented-out option or rpc declaration is never matched.
func readProtoTypeInfo(protoAbsPath, service, method string) (protoTypeInfo, error) {
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
	// Scope to the service the contract declares (service FQN = <pkg>.<Name>),
	// then resolve the method only within that service block — a method name is
	// unique per service, not per file, so a whole-file scan would mis-match a
	// same-named rpc in a sibling service (mirrors protoc-gen-go-grpc walking
	// protogen.Service → method.Input/Output rather than globbing the file).
	simpleService, err := serviceSimpleName(service, pkg)
	if err != nil {
		return protoTypeInfo{}, err
	}
	block, err := extractServiceBlock(text, simpleService)
	if err != nil {
		return protoTypeInfo{}, err
	}
	req, resp, err := parseRPCMethod(block, method, pkg)
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

// serviceSimpleName derives the proto service's simple name from the contract's
// fully-qualified service (endpoints.grpc.service) and validates it lives in the
// proto's own package — the contract's service FQN and the .proto's package
// declaration are two sources that must agree, else codegen would scope to a
// service the proto does not define.
func serviceSimpleName(fqService, protoPkg string) (string, error) {
	prefix := protoPkg + "."
	if !strings.HasPrefix(fqService, prefix) {
		return "", fmt.Errorf("proto: contract service %q is not in proto package %q", fqService, protoPkg)
	}
	simple := strings.TrimPrefix(fqService, prefix)
	if simple == "" || strings.Contains(simple, ".") {
		return "", fmt.Errorf("proto: contract service %q must be %q + a single service name", fqService, prefix)
	}
	return simple, nil
}

// extractServiceBlock returns the body (between the braces) of the named proto
// service, located by brace-matching from `service <name> {`. text must already
// be comment-stripped.
//
// Blind spot (accepted, same class as stripProtoComments): a `{`/`}` inside a
// proto string literal (e.g. an rpc/service option value) would mis-count brace
// depth. Service-block option strings do not contain braces in practice, and
// readProtoTypeInfo only reads in-repo proto files.
func extractServiceBlock(text, serviceName string) (string, error) {
	head := regexp.MustCompile(`(?m)\bservice\s+` + regexp.QuoteMeta(serviceName) + `\s*\{`)
	loc := head.FindStringIndex(text)
	if loc == nil {
		return "", fmt.Errorf("proto: service %q not found", serviceName)
	}
	depth := 1
	for i := loc[1]; i < len(text); i++ {
		switch text[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return text[loc[1]:i], nil
			}
		}
	}
	return "", fmt.Errorf("proto: service %q block has unbalanced braces", serviceName)
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

// parseRPCMethod finds the rpc declaration for method within a service block and
// returns its request / response message simple names. Streaming rpcs are
// rejected (unary only until PR 10); message types must resolve to protoPkg
// (see localMessageName).
func parseRPCMethod(block, method, protoPkg string) (req, resp string, err error) {
	for _, m := range rpcLineRE.FindAllStringSubmatch(block, -1) {
		if m[1] != method {
			continue
		}
		if m[2] != "" || m[4] != "" {
			return "", "", fmt.Errorf("proto: rpc %q is streaming; codegen supports unary only (PR 10)", method)
		}
		req, err := localMessageName(m[3], protoPkg)
		if err != nil {
			return "", "", err
		}
		resp, err := localMessageName(m[5], protoPkg)
		if err != nil {
			return "", "", err
		}
		return req, resp, nil
	}
	return "", "", fmt.Errorf("proto: rpc method %q not found in service block", method)
}

// localMessageName resolves a proto message reference to its simple Go type
// name, fail-closed on cross-package references. An unqualified name is a
// same-package message (returned as-is). A qualified name must be qualified with
// protoPkg itself (proto3 allows a fully-qualified self-reference, optionally
// leading-dotted) — its last segment is the simple name. A reference qualified
// with any OTHER package is an imported message whose Go binding lives under a
// different go_package/import alias; the generated stub fixes the current
// service's alias on every type, so emitting *thisAlias.Foo for a foreign Foo
// would be wrong. Cross-package message types need go_package import resolution
// (deferred); until then they are rejected rather than mis-generated.
func localMessageName(ref, protoPkg string) (string, error) {
	if !strings.Contains(ref, ".") {
		return ref, nil
	}
	ref = strings.TrimPrefix(ref, ".")
	idx := strings.LastIndex(ref, ".")
	qualifier, name := ref[:idx], ref[idx+1:]
	if qualifier != protoPkg {
		return "", fmt.Errorf(
			"proto: message %q is in external package %q (not %q); cross-package message types are not supported yet "+
				"(would require go_package import resolution)",
			ref, qualifier, protoPkg)
	}
	return name, nil
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
