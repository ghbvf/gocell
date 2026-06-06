package contractgen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Fully-qualified service names matching the fixtures' proto package +
// service declarations (ReadProtoServiceInfo scopes the method to this service).
const (
	fixtureFQService = "device.command.v1.DeviceCommandService"
	synthFQServiceS  = "device.command.v1.S"
)

// fixtureProtoPath returns the absolute path to the synth_grpc_minimal proto.
func fixtureProtoPath(t *testing.T) string {
	t.Helper()
	abs, err := filepath.Abs(filepath.Join(
		"testdata", "synth", "synth_grpc_minimal",
		"contracts", "grpc", "device", "command", "v1", "device_command.proto"))
	if err != nil {
		t.Fatalf("abs proto path: %v", err)
	}
	return abs
}

// fixtureMultiProtoPath returns the absolute path to the synth_grpc_multimethod proto.
func fixtureMultiProtoPath(t *testing.T) string {
	t.Helper()
	abs, err := filepath.Abs(filepath.Join(
		"testdata", "synth", "synth_grpc_multimethod",
		"contracts", "grpc", "device", "command", "v1", "device_command.proto"))
	if err != nil {
		t.Fatalf("abs proto path: %v", err)
	}
	return abs
}

// --- Error-branch tests covering shared helpers via ReadProtoServiceInfo ---

func TestReadProtoServiceInfo_MissingGoPackage(t *testing.T) {
	t.Parallel()
	src := `syntax = "proto3";
package device.command.v1;
service DeviceCommandService {
  rpc IssueCommand(IssueCommandRequest) returns (IssueCommandResponse) {}
}
`
	path := writeTempProto(t, src)
	_, err := ReadProtoServiceInfo(path, fixtureFQService)
	if err == nil || !strings.Contains(err.Error(), "go_package") {
		t.Fatalf("expected go_package error, got %v", err)
	}
}

func TestReadProtoServiceInfo_GoPackageMissingAlias(t *testing.T) {
	t.Parallel()
	src := `syntax = "proto3";
package device.command.v1;
option go_package = "github.com/ghbvf/gocell/generated/contracts/grpc/device/command/v1";
service DeviceCommandService {
  rpc IssueCommand(IssueCommandRequest) returns (IssueCommandResponse) {}
}
`
	path := writeTempProto(t, src)
	_, err := ReadProtoServiceInfo(path, fixtureFQService)
	// Distinct from the missing-go_package path: this is the no-";alias" branch.
	if err == nil || !strings.Contains(err.Error(), "must be") {
		t.Fatalf("expected go_package missing-alias (`must be`) error, got %v", err)
	}
}

func TestReadProtoServiceInfo_GoPackageAliasNotIdentifier(t *testing.T) {
	t.Parallel()
	src := `syntax = "proto3";
package device.command.v1;
option go_package = "github.com/ghbvf/gocell/generated/contracts/grpc/device/command/v1;my-alias";
service S {
  rpc IssueCommand(IssueCommandRequest) returns (IssueCommandResponse) {}
}
`
	path := writeTempProto(t, src)
	_, err := ReadProtoServiceInfo(path, synthFQServiceS)
	if err == nil || !strings.Contains(err.Error(), "identifier") {
		t.Fatalf("expected alias-not-identifier error, got %v", err)
	}
}

// TestReadProtoServiceInfo_GoPackagePathInvalid covers module.CheckImportPath rejection.
// Whitespace (space) is invalid per module.CheckImportPath; the error text now
// comes from that authoritative checker rather than our prior whitespace-only guard.
func TestReadProtoServiceInfo_GoPackagePathInvalid(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		path    string
		wantErr string
	}{
		{
			name:    "whitespace in path",
			path:    "github.com/ghbvf/gocell/generated/ grpc",
			wantErr: "invalid",
		},
		{
			name:    "backslash in path",
			path:    `github.com/ghbvf/gocell/generated\grpc`,
			wantErr: "invalid",
		},
		{
			name:    "leading dot segment",
			path:    "./github.com/ghbvf/gocell/generated/grpc",
			wantErr: "invalid",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			src := `syntax = "proto3";
package device.command.v1;
option go_package = "` + tc.path + `;commandv1";
service S {
  rpc IssueCommand(IssueCommandRequest) returns (IssueCommandResponse) {}
}
`
			path := writeTempProto(t, src)
			_, err := ReadProtoServiceInfo(path, synthFQServiceS)
			if err == nil {
				t.Fatalf("expected error for invalid import path %q, got nil", tc.path)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error %q does not contain %q", err.Error(), tc.wantErr)
			}
		})
	}
}

func TestReadProtoServiceInfo_MissingPackage(t *testing.T) {
	t.Parallel()
	src := `syntax = "proto3";
option go_package = "github.com/ghbvf/gocell/generated/contracts/grpc/device/command/v1;commandv1";
service DeviceCommandService {
  rpc IssueCommand(IssueCommandRequest) returns (IssueCommandResponse) {}
}
`
	path := writeTempProto(t, src)
	_, err := ReadProtoServiceInfo(path, fixtureFQService)
	if err == nil || !strings.Contains(err.Error(), "package") {
		t.Fatalf("expected package error, got %v", err)
	}
}

func TestReadProtoServiceInfo_ServiceNotInDeclaredPackage(t *testing.T) {
	t.Parallel()
	_, err := ReadProtoServiceInfo(fixtureProtoPath(t), "wrong.pkg.DeviceCommandService")
	if err == nil || !strings.Contains(err.Error(), "not in proto package") {
		t.Fatalf("expected service-package-mismatch error, got %v", err)
	}
}

// TestReadProtoServiceInfo_ExternalQualifiedMessageRejected (F2 regression) proves a
// message qualified with a foreign proto package is rejected fail-closed rather
// than mis-bound to the current service's import alias.
func TestReadProtoServiceInfo_ExternalQualifiedMessageRejected(t *testing.T) {
	t.Parallel()
	src := `syntax = "proto3";
package device.command.v1;
option go_package = "github.com/ghbvf/gocell/generated/contracts/grpc/device/command/v1;commandv1";
service S {
  rpc IssueCommand(other.pkg.ForeignRequest) returns (IssueCommandResponse) {}
}
`
	path := writeTempProto(t, src)
	_, err := ReadProtoServiceInfo(path, synthFQServiceS)
	if err == nil || !strings.Contains(err.Error(), "external package") {
		t.Fatalf("expected external-package rejection, got %v", err)
	}
}

// --- Migrated happy-path / scoping tests ---

// TestReadProtoServiceInfo_CommentedRPCIgnored proves comment stripping: a
// commented-out rpc line must not be matched, and a block comment must not hide
// the real declaration.
func TestReadProtoServiceInfo_CommentedRPCIgnored(t *testing.T) {
	t.Parallel()
	src := `syntax = "proto3";
package device.command.v1;
option go_package = "github.com/ghbvf/gocell/generated/contracts/grpc/device/command/v1;commandv1";
service S {
  // rpc IssueCommand(WrongRequest) returns (WrongResponse) {}
  /* rpc IssueCommand(AlsoWrong) returns (AlsoWrong) {} */
  rpc IssueCommand(IssueCommandRequest) returns (IssueCommandResponse) {}
}
`
	path := writeTempProto(t, src)
	info, err := ReadProtoServiceInfo(path, synthFQServiceS)
	if err != nil {
		t.Fatalf("ReadProtoServiceInfo: %v", err)
	}
	if len(info.Methods) == 0 {
		t.Fatal("expected at least one method")
	}
	if info.Methods[0].RequestType != "IssueCommandRequest" || info.Methods[0].ResponseType != "IssueCommandResponse" {
		t.Errorf("comment stripping failed: matched %q/%q", info.Methods[0].RequestType, info.Methods[0].ResponseType)
	}
}

// TestReadProtoServiceInfo_PackageQualifiedMessage accepts a message qualified with
// the proto's OWN package (proto allows `.pkg.Msg` self-references), resolving it
// to the simple name.
func TestReadProtoServiceInfo_PackageQualifiedMessage(t *testing.T) {
	t.Parallel()
	src := `syntax = "proto3";
package device.command.v1;
option go_package = "github.com/ghbvf/gocell/generated/contracts/grpc/device/command/v1;commandv1";
service S {
  rpc IssueCommand(device.command.v1.IssueCommandRequest) returns (device.command.v1.IssueCommandResponse) {}
}
`
	path := writeTempProto(t, src)
	info, err := ReadProtoServiceInfo(path, synthFQServiceS)
	if err != nil {
		t.Fatalf("ReadProtoServiceInfo: %v", err)
	}
	if len(info.Methods) == 0 {
		t.Fatal("expected at least one method")
	}
	if info.Methods[0].RequestType != "IssueCommandRequest" || info.Methods[0].ResponseType != "IssueCommandResponse" {
		t.Errorf("package-qualifier strip failed: %q/%q", info.Methods[0].RequestType, info.Methods[0].ResponseType)
	}
}

// TestReadProtoServiceInfo_ServiceScoped (F1 regression) proves the method set is
// resolved within the CONTRACT'S declared service, not the first file-wide match:
// a sibling service's methods must not be included.
func TestReadProtoServiceInfo_ServiceScoped(t *testing.T) {
	t.Parallel()
	src := `syntax = "proto3";
package device.command.v1;
option go_package = "github.com/ghbvf/gocell/generated/contracts/grpc/device/command/v1;commandv1";
service OtherService {
  rpc IssueCommand(OtherRequest) returns (OtherResponse) {}
}
service DeviceCommandService {
  rpc IssueCommand(IssueCommandRequest) returns (IssueCommandResponse) {}
}
`
	path := writeTempProto(t, src)
	info, err := ReadProtoServiceInfo(path, fixtureFQService)
	if err != nil {
		t.Fatalf("ReadProtoServiceInfo: %v", err)
	}
	if len(info.Methods) == 0 {
		t.Fatal("expected at least one method")
	}
	// Must pick DeviceCommandService's types, NOT OtherService's (which appears
	// first in the file and would win a whole-file scan).
	if info.Methods[0].RequestType != "IssueCommandRequest" || info.Methods[0].ResponseType != "IssueCommandResponse" {
		t.Errorf("service scoping failed: matched %q/%q (sibling service leaked)", info.Methods[0].RequestType, info.Methods[0].ResponseType)
	}
}

// TestReadProtoServiceInfo_ExternalResponseMessageRejected proves the guard is
// symmetric: a RESPONSE message qualified with a foreign proto package is
// rejected fail-closed, mirroring the request-side rejection (the generated stub
// fixes one import alias, so a foreign response type would be mis-bound).
func TestReadProtoServiceInfo_ExternalResponseMessageRejected(t *testing.T) {
	t.Parallel()
	src := `syntax = "proto3";
package device.command.v1;
option go_package = "github.com/ghbvf/gocell/generated/contracts/grpc/device/command/v1;commandv1";
service DeviceCommandService {
  rpc IssueCommand(IssueCommandRequest) returns (other.pkg.ForeignResponse) {}
}
`
	path := writeTempProto(t, src)
	_, err := ReadProtoServiceInfo(path, fixtureFQService)
	if err == nil || !strings.Contains(err.Error(), "external package") {
		t.Fatalf("expected external-package rejection on response type, got %v", err)
	}
}

// --- Existing ReadProtoServiceInfo tests (service-level, #1655) ---

// TestReadProtoServiceInfo_SingleMethod verifies synth_grpc_minimal (one RPC):
// the returned ProtoServiceInfo has exactly one method with the correct types.
func TestReadProtoServiceInfo_SingleMethod(t *testing.T) {
	t.Parallel()
	info, err := ReadProtoServiceInfo(fixtureProtoPath(t), fixtureFQService)
	if err != nil {
		t.Fatalf("ReadProtoServiceInfo: %v", err)
	}
	if info.ProtoPackage != "device.command.v1" {
		t.Errorf("ProtoPackage = %q, want %q", info.ProtoPackage, "device.command.v1")
	}
	if info.ImportPath != "github.com/ghbvf/gocell/generated/contracts/grpc/device/command/v1" {
		t.Errorf("ImportPath = %q", info.ImportPath)
	}
	if info.Alias != "commandv1" {
		t.Errorf("Alias = %q, want %q", info.Alias, "commandv1")
	}
	if len(info.Methods) != 1 {
		t.Fatalf("len(Methods) = %d, want 1; got %+v", len(info.Methods), info.Methods)
	}
	m := info.Methods[0]
	if m.Name != "IssueCommand" {
		t.Errorf("Methods[0].Name = %q, want %q", m.Name, "IssueCommand")
	}
	if m.RequestType != "IssueCommandRequest" {
		t.Errorf("Methods[0].RequestType = %q, want %q", m.RequestType, "IssueCommandRequest")
	}
	if m.ResponseType != "IssueCommandResponse" {
		t.Errorf("Methods[0].ResponseType = %q, want %q", m.ResponseType, "IssueCommandResponse")
	}
}

// TestReadProtoServiceInfo_MultiMethod verifies synth_grpc_multimethod (two RPCs):
// the returned Methods slice has both entries in declaration order.
func TestReadProtoServiceInfo_MultiMethod(t *testing.T) {
	t.Parallel()
	info, err := ReadProtoServiceInfo(fixtureMultiProtoPath(t), fixtureFQService)
	if err != nil {
		t.Fatalf("ReadProtoServiceInfo: %v", err)
	}
	if len(info.Methods) != 2 {
		t.Fatalf("len(Methods) = %d, want 2; got %+v", len(info.Methods), info.Methods)
	}
	wantMethods := []ProtoMethodInfo{
		{Name: "IssueCommand", RequestType: "IssueCommandRequest", ResponseType: "IssueCommandResponse"},
		{Name: "GetCommandStatus", RequestType: "GetCommandStatusRequest", ResponseType: "GetCommandStatusResponse"},
	}
	for i, want := range wantMethods {
		got := info.Methods[i]
		if got != want {
			t.Errorf("Methods[%d] = %+v, want %+v", i, got, want)
		}
	}
}

// TestReadProtoServiceInfo_FileUnreadable ensures a missing file returns an error.
func TestReadProtoServiceInfo_FileUnreadable(t *testing.T) {
	t.Parallel()
	_, err := ReadProtoServiceInfo(filepath.Join(t.TempDir(), "missing.proto"), fixtureFQService)
	if err == nil {
		t.Fatal("expected error for missing proto file")
	}
}

// TestReadProtoServiceInfo_ServiceNotFound ensures an unknown service name returns an error.
func TestReadProtoServiceInfo_ServiceNotFound(t *testing.T) {
	t.Parallel()
	_, err := ReadProtoServiceInfo(fixtureProtoPath(t), "device.command.v1.NoSuchService")
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("expected service-not-found error, got %v", err)
	}
}

// TestReadProtoServiceInfo_StreamingRejected verifies that a service containing
// any streaming RPC is rejected — service-level codegen supports unary only.
// The rpc grammar has two independent stream positions (request-side and
// response-side), so all three non-unary shapes must be covered: server-stream
// (response only), client-stream (request only), and bidi (both).
func TestReadProtoServiceInfo_StreamingRejected(t *testing.T) {
	t.Parallel()
	const header = `syntax = "proto3";
package device.command.v1;
option go_package = "github.com/ghbvf/gocell/generated/contracts/grpc/device/command/v1;commandv1";
`
	cases := []struct {
		name string
		rpc  string
	}{
		{"server-stream", "rpc Watch(WatchRequest) returns (stream WatchResponse) {}"},
		{"client-stream", "rpc Upload(stream UploadRequest) returns (UploadResponse) {}"},
		{"bidi", "rpc Chat(stream ChatRequest) returns (stream ChatResponse) {}"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			src := header + "service DeviceCommandService {\n  " + tc.rpc + "\n}\n"
			path := writeTempProto(t, src)
			_, err := ReadProtoServiceInfo(path, fixtureFQService)
			if err == nil || !strings.Contains(err.Error(), "streaming") {
				t.Fatalf("expected streaming-rejected error, got %v", err)
			}
		})
	}
}

// TestReadProtoServiceInfo_DuplicateMethodRejected verifies that two rpc
// declarations with the same method name in one service block are rejected —
// the regex reader does not get protoc's own duplicate-rpc check, so without
// this guard the duplicate would surface as an uncompilable generated interface.
func TestReadProtoServiceInfo_DuplicateMethodRejected(t *testing.T) {
	t.Parallel()
	src := `syntax = "proto3";
package device.command.v1;
option go_package = "github.com/ghbvf/gocell/generated/contracts/grpc/device/command/v1;commandv1";
service DeviceCommandService {
  rpc IssueCommand(IssueCommandRequest) returns (IssueCommandResponse) {}
  rpc IssueCommand(IssueCommandRequest) returns (IssueCommandResponse) {}
}
`
	path := writeTempProto(t, src)
	_, err := ReadProtoServiceInfo(path, fixtureFQService)
	if err == nil || !strings.Contains(err.Error(), "duplicate rpc method") {
		t.Fatalf("expected duplicate-method error, got %v", err)
	}
}

// TestReadProtoServiceInfo_EmptyServiceBlock verifies that a service with no rpc
// declarations is rejected (service block cannot be empty in practice).
func TestReadProtoServiceInfo_EmptyServiceBlock(t *testing.T) {
	t.Parallel()
	src := `syntax = "proto3";
package device.command.v1;
option go_package = "github.com/ghbvf/gocell/generated/contracts/grpc/device/command/v1;commandv1";
service DeviceCommandService {
}
`
	path := writeTempProto(t, src)
	_, err := ReadProtoServiceInfo(path, fixtureFQService)
	if err == nil || !strings.Contains(err.Error(), "no rpc") {
		t.Fatalf("expected no-rpc error, got %v", err)
	}
}

// TestReadProtoServiceInfo_UnexportedMethodRejected verifies that an rpc method
// with a lowercase name (not an exported Go identifier) is rejected — codegen
// renders each method name directly as a Go interface method.
func TestReadProtoServiceInfo_UnexportedMethodRejected(t *testing.T) {
	t.Parallel()
	src := `syntax = "proto3";
package device.command.v1;
option go_package = "github.com/ghbvf/gocell/generated/contracts/grpc/device/command/v1;commandv1";
service DeviceCommandService {
  rpc issueCommand(IssueCommandRequest) returns (IssueCommandResponse) {}
}
`
	path := writeTempProto(t, src)
	_, err := ReadProtoServiceInfo(path, fixtureFQService)
	if err == nil || !strings.Contains(err.Error(), "exported Go identifier") {
		t.Fatalf("expected exported-identifier error, got %v", err)
	}
}

func writeTempProto(t *testing.T, src string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "x.proto")
	if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
		t.Fatalf("write temp proto: %v", err)
	}
	return path
}

func TestIndexLineComment(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   string
		want int
	}{
		{"plain comment", "  rpc X() // tail", 10},
		{"no comment", "  rpc X()", -1},
		{"slashes inside string not a comment", `opt = "a//b";`, -1},
		{"comment after string", `opt = "a"; // c`, 11},
		{"leading comment", "// whole line", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := indexLineComment(tc.in); got != tc.want {
				t.Errorf("indexLineComment(%q) = %d, want %d", tc.in, got, tc.want)
			}
		})
	}
}

func TestLocalMessageName(t *testing.T) {
	t.Parallel()
	const pkg = "device.command.v1"
	cases := []struct {
		name    string
		ref     string
		want    string
		wantErr bool
	}{
		{"unqualified", "Msg", "Msg", false},
		{"same-package qualified", "device.command.v1.Msg", "Msg", false},
		{"same-package leading-dot", ".device.command.v1.Msg", "Msg", false},
		{"external package rejected", "other.pkg.Msg", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := localMessageName(tc.ref, pkg)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("localMessageName(%q) expected error", tc.ref)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Errorf("localMessageName(%q) = %q, %v; want %q, nil", tc.ref, got, err, tc.want)
			}
		})
	}
}
