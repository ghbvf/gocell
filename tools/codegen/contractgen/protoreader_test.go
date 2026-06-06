package contractgen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Fully-qualified service names matching the fixtures' proto package +
// service declarations (ReadProtoTypeInfo scopes the method to this service).
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

func TestReadProtoTypeInfo_HappyPath(t *testing.T) {
	t.Parallel()
	info, err := ReadProtoTypeInfo(fixtureProtoPath(t), fixtureFQService, "IssueCommand")
	if err != nil {
		t.Fatalf("ReadProtoTypeInfo: %v", err)
	}
	want := ProtoTypeInfo{
		ProtoPackage: "device.command.v1",
		ImportPath:   "github.com/ghbvf/gocell/generated/contracts/grpc/device/command/v1",
		Alias:        "commandv1",
		RequestType:  "IssueCommandRequest",
		ResponseType: "IssueCommandResponse",
	}
	if info != want {
		t.Errorf("protoTypeInfo mismatch:\n got: %+v\nwant: %+v", info, want)
	}
}

func TestReadProtoTypeInfo_FileUnreadable(t *testing.T) {
	t.Parallel()
	_, err := ReadProtoTypeInfo(filepath.Join(t.TempDir(), "missing.proto"), fixtureFQService, "IssueCommand")
	if err == nil {
		t.Fatal("expected error for missing proto file")
	}
}

func TestReadProtoTypeInfo_MissingGoPackage(t *testing.T) {
	t.Parallel()
	src := `syntax = "proto3";
package device.command.v1;
service DeviceCommandService {
  rpc IssueCommand(IssueCommandRequest) returns (IssueCommandResponse) {}
}
`
	path := writeTempProto(t, src)
	_, err := ReadProtoTypeInfo(path, fixtureFQService, "IssueCommand")
	if err == nil || !strings.Contains(err.Error(), "go_package") {
		t.Fatalf("expected go_package error, got %v", err)
	}
}

func TestReadProtoTypeInfo_GoPackageMissingAlias(t *testing.T) {
	t.Parallel()
	src := `syntax = "proto3";
package device.command.v1;
option go_package = "github.com/ghbvf/gocell/generated/contracts/grpc/device/command/v1";
service DeviceCommandService {
  rpc IssueCommand(IssueCommandRequest) returns (IssueCommandResponse) {}
}
`
	path := writeTempProto(t, src)
	_, err := ReadProtoTypeInfo(path, fixtureFQService, "IssueCommand")
	// Distinct from the missing-go_package path: this is the no-";alias" branch.
	if err == nil || !strings.Contains(err.Error(), "must be") {
		t.Fatalf("expected go_package missing-alias (`must be`) error, got %v", err)
	}
}

func TestReadProtoTypeInfo_GoPackageAliasNotIdentifier(t *testing.T) {
	t.Parallel()
	src := `syntax = "proto3";
package device.command.v1;
option go_package = "github.com/ghbvf/gocell/generated/contracts/grpc/device/command/v1;my-alias";
service S {
  rpc IssueCommand(IssueCommandRequest) returns (IssueCommandResponse) {}
}
`
	path := writeTempProto(t, src)
	_, err := ReadProtoTypeInfo(path, synthFQServiceS, "IssueCommand")
	if err == nil || !strings.Contains(err.Error(), "identifier") {
		t.Fatalf("expected alias-not-identifier error, got %v", err)
	}
}

// TestReadProtoTypeInfo_GoPackagePathInvalid covers module.CheckImportPath rejection.
// Whitespace (space) is invalid per module.CheckImportPath; the error text now
// comes from that authoritative checker rather than our prior whitespace-only guard.
func TestReadProtoTypeInfo_GoPackagePathInvalid(t *testing.T) {
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
			_, err := ReadProtoTypeInfo(path, synthFQServiceS, "IssueCommand")
			if err == nil {
				t.Fatalf("expected error for invalid import path %q, got nil", tc.path)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error %q does not contain %q", err.Error(), tc.wantErr)
			}
		})
	}
}

func TestReadProtoTypeInfo_MissingPackage(t *testing.T) {
	t.Parallel()
	src := `syntax = "proto3";
option go_package = "github.com/ghbvf/gocell/generated/contracts/grpc/device/command/v1;commandv1";
service DeviceCommandService {
  rpc IssueCommand(IssueCommandRequest) returns (IssueCommandResponse) {}
}
`
	path := writeTempProto(t, src)
	_, err := ReadProtoTypeInfo(path, fixtureFQService, "IssueCommand")
	if err == nil || !strings.Contains(err.Error(), "package") {
		t.Fatalf("expected package error, got %v", err)
	}
}

func TestReadProtoTypeInfo_MethodNotFound(t *testing.T) {
	t.Parallel()
	_, err := ReadProtoTypeInfo(fixtureProtoPath(t), fixtureFQService, "Ghost")
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("expected method-not-found error, got %v", err)
	}
}

func TestReadProtoTypeInfo_StreamingRejected(t *testing.T) {
	t.Parallel()
	src := `syntax = "proto3";
package device.command.v1;
option go_package = "github.com/ghbvf/gocell/generated/contracts/grpc/device/command/v1;commandv1";
service S {
  rpc Watch(WatchRequest) returns (stream WatchResponse) {}
}
`
	path := writeTempProto(t, src)
	_, err := ReadProtoTypeInfo(path, synthFQServiceS, "Watch")
	if err == nil || !strings.Contains(err.Error(), "streaming") {
		t.Fatalf("expected streaming-rejected error, got %v", err)
	}
}

// TestReadProtoTypeInfo_CommentedRPCIgnored proves comment stripping: a
// commented-out rpc line with the same method name must not be matched, and a
// block comment must not hide the real declaration.
func TestReadProtoTypeInfo_CommentedRPCIgnored(t *testing.T) {
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
	info, err := ReadProtoTypeInfo(path, synthFQServiceS, "IssueCommand")
	if err != nil {
		t.Fatalf("ReadProtoTypeInfo: %v", err)
	}
	if info.RequestType != "IssueCommandRequest" || info.ResponseType != "IssueCommandResponse" {
		t.Errorf("comment stripping failed: matched %q/%q", info.RequestType, info.ResponseType)
	}
}

// TestReadProtoTypeInfo_PackageQualifiedMessage accepts a message qualified with
// the proto's OWN package (proto allows `.pkg.Msg` self-references), resolving it
// to the simple name.
func TestReadProtoTypeInfo_PackageQualifiedMessage(t *testing.T) {
	t.Parallel()
	src := `syntax = "proto3";
package device.command.v1;
option go_package = "github.com/ghbvf/gocell/generated/contracts/grpc/device/command/v1;commandv1";
service S {
  rpc IssueCommand(device.command.v1.IssueCommandRequest) returns (device.command.v1.IssueCommandResponse) {}
}
`
	path := writeTempProto(t, src)
	info, err := ReadProtoTypeInfo(path, synthFQServiceS, "IssueCommand")
	if err != nil {
		t.Fatalf("ReadProtoTypeInfo: %v", err)
	}
	if info.RequestType != "IssueCommandRequest" || info.ResponseType != "IssueCommandResponse" {
		t.Errorf("package-qualifier strip failed: %q/%q", info.RequestType, info.ResponseType)
	}
}

// TestReadProtoTypeInfo_ServiceScoped (F1 regression) proves the method is
// resolved within the CONTRACT'S declared service, not the first file-wide
// match: a sibling service with the same method name must not shadow it.
func TestReadProtoTypeInfo_ServiceScoped(t *testing.T) {
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
	info, err := ReadProtoTypeInfo(path, fixtureFQService, "IssueCommand")
	if err != nil {
		t.Fatalf("ReadProtoTypeInfo: %v", err)
	}
	// Must pick DeviceCommandService's types, NOT OtherService's (which appears
	// first in the file and would win a whole-file scan).
	if info.RequestType != "IssueCommandRequest" || info.ResponseType != "IssueCommandResponse" {
		t.Errorf("service scoping failed: matched %q/%q (sibling service leaked)", info.RequestType, info.ResponseType)
	}
}

// TestReadProtoTypeInfo_MethodInOtherServiceNotFound (F1 regression) proves a
// method that exists only in a NON-declared service is not found.
func TestReadProtoTypeInfo_MethodInOtherServiceNotFound(t *testing.T) {
	t.Parallel()
	src := `syntax = "proto3";
package device.command.v1;
option go_package = "github.com/ghbvf/gocell/generated/contracts/grpc/device/command/v1;commandv1";
service OtherService {
  rpc IssueCommand(OtherRequest) returns (OtherResponse) {}
}
service DeviceCommandService {
  rpc RevokeCommand(RevokeRequest) returns (RevokeResponse) {}
}
`
	path := writeTempProto(t, src)
	_, err := ReadProtoTypeInfo(path, fixtureFQService, "IssueCommand")
	if err == nil || !strings.Contains(err.Error(), "not found in service block") {
		t.Fatalf("expected method-not-in-declared-service error, got %v", err)
	}
}

// TestReadProtoTypeInfo_ExternalQualifiedMessageRejected (F2 regression) proves a
// message qualified with a foreign proto package is rejected fail-closed rather
// than mis-bound to the current service's import alias.
func TestReadProtoTypeInfo_ExternalQualifiedMessageRejected(t *testing.T) {
	t.Parallel()
	src := `syntax = "proto3";
package device.command.v1;
option go_package = "github.com/ghbvf/gocell/generated/contracts/grpc/device/command/v1;commandv1";
service S {
  rpc IssueCommand(other.pkg.ForeignRequest) returns (IssueCommandResponse) {}
}
`
	path := writeTempProto(t, src)
	_, err := ReadProtoTypeInfo(path, synthFQServiceS, "IssueCommand")
	if err == nil || !strings.Contains(err.Error(), "external package") {
		t.Fatalf("expected external-package rejection, got %v", err)
	}
}

func TestReadProtoTypeInfo_ServiceNotInDeclaredPackage(t *testing.T) {
	t.Parallel()
	_, err := ReadProtoTypeInfo(fixtureProtoPath(t), "wrong.pkg.DeviceCommandService", "IssueCommand")
	if err == nil || !strings.Contains(err.Error(), "not in proto package") {
		t.Fatalf("expected service-package-mismatch error, got %v", err)
	}
}

func TestReadProtoTypeInfo_ServiceBlockNotFound(t *testing.T) {
	t.Parallel()
	_, err := ReadProtoTypeInfo(fixtureProtoPath(t), "device.command.v1.NoSuchService", "IssueCommand")
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("expected service-not-found error, got %v", err)
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
