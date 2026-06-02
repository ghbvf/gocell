package contractgen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
	info, err := readProtoTypeInfo(fixtureProtoPath(t), "IssueCommand")
	if err != nil {
		t.Fatalf("readProtoTypeInfo: %v", err)
	}
	want := protoTypeInfo{
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
	_, err := readProtoTypeInfo(filepath.Join(t.TempDir(), "missing.proto"), "IssueCommand")
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
	_, err := readProtoTypeInfo(path, "IssueCommand")
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
	_, err := readProtoTypeInfo(path, "IssueCommand")
	if err == nil || !strings.Contains(err.Error(), "go_package") {
		t.Fatalf("expected go_package alias error, got %v", err)
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
	_, err := readProtoTypeInfo(path, "IssueCommand")
	if err == nil || !strings.Contains(err.Error(), "package") {
		t.Fatalf("expected package error, got %v", err)
	}
}

func TestReadProtoTypeInfo_MethodNotFound(t *testing.T) {
	t.Parallel()
	_, err := readProtoTypeInfo(fixtureProtoPath(t), "Ghost")
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
	_, err := readProtoTypeInfo(path, "Watch")
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
	info, err := readProtoTypeInfo(path, "IssueCommand")
	if err != nil {
		t.Fatalf("readProtoTypeInfo: %v", err)
	}
	if info.RequestType != "IssueCommandRequest" || info.ResponseType != "IssueCommandResponse" {
		t.Errorf("comment stripping failed: matched %q/%q", info.RequestType, info.ResponseType)
	}
}

// TestReadProtoTypeInfo_PackageQualifiedMessage strips a leading package
// qualifier from message names (proto allows `.pkg.Msg` references).
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
	info, err := readProtoTypeInfo(path, "IssueCommand")
	if err != nil {
		t.Fatalf("readProtoTypeInfo: %v", err)
	}
	if info.RequestType != "IssueCommandRequest" || info.ResponseType != "IssueCommandResponse" {
		t.Errorf("package-qualifier strip failed: %q/%q", info.RequestType, info.ResponseType)
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
