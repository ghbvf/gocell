package contractgen

import (
	"fmt"
	"go/token"
	"os"
	"regexp"
	"strings"

	"golang.org/x/mod/module"
)

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

// ProtoServiceInfo is the proto service identity resolved from a .proto file:
// the Go import binding (from the file's go_package option) plus every RPC
// method declared in the service block. Used by contractgen's service-level
// codegen path (#1655) where one contract owns a whole proto service.
type ProtoServiceInfo struct {
	// ProtoPackage is the proto `package` declaration, e.g. "device.command.v1".
	ProtoPackage string
	// ImportPath is the go_package import path (the part before ';'),
	// e.g. "github.com/ghbvf/gocell/generated/contracts/grpc/device/command/v1".
	ImportPath string
	// Alias is the go_package import alias (the part after ';'), e.g. "commandv1".
	Alias string
	// Methods lists every unary RPC in the service block; streaming RPCs and
	// unexported-identifier method names are rejected (see parseAllRPCMethods).
	Methods []ProtoMethodInfo
}

// ProtoMethodInfo is the name + request/response type for one RPC.
type ProtoMethodInfo struct {
	// Name is the rpc method name, e.g. "IssueCommand".
	Name string
	// RequestType is the request message simple name, e.g. "IssueCommandRequest".
	RequestType string
	// ResponseType is the response message simple name, e.g. "IssueCommandResponse".
	ResponseType string
}

// ReadProtoServiceInfo reads protoAbsPath and returns all unary RPC methods for
// the named service (fully-qualified, e.g. "device.command.v1.DeviceCommandService").
// It enumerates ALL methods in the service block; the proto is the single source
// of truth for the method set (#1655).
//
// Usage: callers must pass an absolute proto path that already exists on disk.
// Contract-context errors should be wrapped by the caller.
func ReadProtoServiceInfo(protoAbsPath, service string) (ProtoServiceInfo, error) {
	raw, err := os.ReadFile(protoAbsPath) // #nosec G304 — codegen reads a contracts-relative .proto resolved by the builder
	if err != nil {
		return ProtoServiceInfo{}, fmt.Errorf("read proto %q: %w", protoAbsPath, err)
	}
	text := stripProtoComments(string(raw))

	pkg, err := parseProtoPackage(text)
	if err != nil {
		return ProtoServiceInfo{}, err
	}
	importPath, alias, err := parseGoPackage(text)
	if err != nil {
		return ProtoServiceInfo{}, err
	}
	simpleService, err := serviceSimpleName(service, pkg)
	if err != nil {
		return ProtoServiceInfo{}, err
	}
	block, err := extractServiceBlock(text, simpleService)
	if err != nil {
		return ProtoServiceInfo{}, err
	}
	methods, err := parseAllRPCMethods(block, pkg)
	if err != nil {
		return ProtoServiceInfo{}, err
	}
	return ProtoServiceInfo{
		ProtoPackage: pkg,
		ImportPath:   importPath,
		Alias:        alias,
		Methods:      methods,
	}, nil
}

// parseAllRPCMethods enumerates every rpc declaration in a service block,
// returning a ProtoMethodInfo for each. Streaming rpcs are rejected (unary
// only). Each method name must be an exported Go identifier — codegen renders
// it directly as a Go interface method. Duplicate method names within the
// service are rejected here rather than left to surface as a duplicate-method
// Go compile error in the generated interface (this regex reader does not get
// protoc's own duplicate-rpc check, so a malformed proto generated without buf
// would otherwise emit an uncompilable interface). The returned slice preserves
// proto declaration order.
func parseAllRPCMethods(block, protoPkg string) ([]ProtoMethodInfo, error) {
	var methods []ProtoMethodInfo
	seen := make(map[string]struct{})
	for _, m := range rpcLineRE.FindAllStringSubmatch(block, -1) {
		name := m[1]
		if m[2] != "" || m[4] != "" {
			return nil, fmt.Errorf("proto: rpc %q is streaming; codegen supports unary only (streaming tracked at gh #1099)", name)
		}
		if !token.IsIdentifier(name) || !token.IsExported(name) {
			return nil, fmt.Errorf("proto: rpc method %q must be an exported Go identifier", name)
		}
		if _, dup := seen[name]; dup {
			return nil, fmt.Errorf("proto: duplicate rpc method %q in service block", name)
		}
		seen[name] = struct{}{}
		req, err := localMessageName(m[3], protoPkg)
		if err != nil {
			return nil, err
		}
		resp, err := localMessageName(m[5], protoPkg)
		if err != nil {
			return nil, err
		}
		methods = append(methods, ProtoMethodInfo{
			Name:         name,
			RequestType:  req,
			ResponseType: resp,
		})
	}
	if len(methods) == 0 {
		return nil, fmt.Errorf("proto: service block contains no rpc declarations")
	}
	return methods, nil
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
// ReadProtoServiceInfo only reads in-repo proto files.
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
// proto-pointing one), and the alias must be a valid Go identifier.
func parseGoPackage(text string) (importPath, alias string, err error) {
	m := goPackageRE.FindStringSubmatch(text)
	if m == nil {
		return "", "", fmt.Errorf("proto: missing `option go_package`")
	}
	path, alias, ok := strings.Cut(m[1], ";")
	if !ok || path == "" || alias == "" {
		return "", "", fmt.Errorf(`proto: option go_package %q must be "<import-path>;<alias>"`, m[1])
	}
	if err := module.CheckImportPath(path); err != nil {
		return "", "", fmt.Errorf("proto: go_package import path %q invalid: %w", path, err)
	}
	if !token.IsIdentifier(alias) {
		return "", "", fmt.Errorf("proto: go_package alias %q must be a valid Go identifier", alias)
	}
	return path, alias, nil
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
				"(would require go_package import resolution; tracked at gh #1099)",
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
// fixture controls the grammar; ReadProtoServiceInfo only reads contracts-relative
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
