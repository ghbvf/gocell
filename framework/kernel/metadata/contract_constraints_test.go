package metadata_test

import (
	"strings"
	"testing"

	"github.com/ghbvf/gocell/framework/kernel/metadata"
)

type grpcServiceGoNameCase struct {
	name     string
	service  string
	wantName string
	wantErr  string // substring; "" = expect nil error
}

// TestMatchCellID covers the CellIDPattern regex semantics (^[a-z][a-z0-9]{1,31}$):
// lowercase ASCII letters + digits only, 2-32 chars, must start with a letter.
// Identical to AssemblyIDPattern by design — the no-dash convention enforced
// by FMT-16 / FMT-C1. 32-char upper bound feeds healthz composed-name budget
// (see pkg/scaffoldid.IdentifierPattern godoc).
func TestMatchCellID(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		in   string
		want bool
	}{
		// Valid cases — real-world cell ids in this repository.
		{"accesscore", "accesscore", true},
		{"auditcore", "auditcore", true},
		{"configcore", "configcore", true},
		{"two_char_min", "ab", true},
		{"letter_plus_digit", "a0", true},
		{"long_concat", "mdmgateway", true},
		{"trailing_digits", "core1", true},

		// Invalid cases.
		{"empty", "", false},
		{"single_char", "a", false},
		{"leading_dash_disallowed", "-foo", false},
		{"internal_dash_disallowed", "foo-bar", false},
		{"trailing_dash_disallowed", "foo-", false},
		{"uppercase_disallowed", "FooBar", false},
		{"mixed_case_disallowed", "fooBar", false},
		{"underscore_disallowed", "foo_bar", false},
		{"digit_start_disallowed", "1foo", false},
		{"whitespace_disallowed", "foo bar", false},
		{"slash_disallowed", "foo/bar", false},
		{"colon_disallowed", "foo:bar", false},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := metadata.MatchCellID(tc.in)
			if got != tc.want {
				t.Fatalf("MatchCellID(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

// TestIsKnownCapability covers the CapabilityEnum predicate — only the
// three closed-set values are accepted; unknown strings and empty string
// are rejected.
func TestIsKnownCapability(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		in   string
		want bool
	}{
		// Valid — closed enum members.
		{"postgres", "postgres", true},
		{"redis", "redis", true},
		{"rabbitmq", "rabbitmq", true},

		// Invalid — out-of-enum values.
		{"empty", "", false},
		{"foobar", "foobar", false},
		{"mysql", "mysql", false},
		{"POSTGRES_upper", "POSTGRES", false},
		{"with_space", "post gres", false},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := metadata.IsKnownCapability(tc.in)
			if got != tc.want {
				t.Fatalf("IsKnownCapability(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

// TestIsValidMetadataText covers the bool predicate for free-text metadata
// fields (owner.team, owner.role, etc.) — rejects the control characters
// that would break inline YAML scalar emission or fabricate adjacent fields:
// \n (LF), \r (CR), \x00 (NUL), \t (tab). All other characters are accepted;
// full YAML safety is delegated to pkg/yamlsafe.Quote at the rendering
// boundary. Tab is included because YAML 1.2 §5.1 allows tab in plain
// scalars so yamlsafe.Quote would not add quotes, letting the tab reach the
// generated YAML (validateScaffoldID / validateScaffoldText #8 parity).
//
// Mirrors the K8s apimachinery IsDNS1123Label predicate style: a single
// bool helper exported from kernel/metadata as the syntactic constraint
// single source; callers compose their own errcode wrapping.
func TestIsValidMetadataText(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		in   string
		want bool
	}{
		// Valid — common free-text shapes.
		{"empty_accepted", "", true},
		{"ascii_word", "platform", true},
		{"ascii_with_space", "platform engineering", true},
		{"ascii_with_dash", "site-reliability", true},
		{"ascii_with_colon", "team:platform", true}, // colon is YAML-safe via yamlsafe.Quote
		{"unicode_letters", "技术平台", true},
		{"emoji_ok", "team-🚀", true},
		{"punctuation_ok", "foo,bar;baz", true},

		// Invalid — control characters that break YAML scalar emission.
		{"lf_rejected", "alice\nbob", false},
		{"cr_rejected", "alice\rbob", false},
		{"crlf_rejected", "alice\r\nbob", false},
		{"nul_rejected", "alice\x00bob", false},
		{"trailing_lf_rejected", "alice\n", false},
		{"leading_lf_rejected", "\nalice", false},
		{"only_lf_rejected", "\n", false},
		// Tab rejected — YAML 1.2 §5.1 allows tab in plain scalars, so
		// yamlsafe.Quote would not quote it; must be blocked at this layer.
		{"tab_rejected", "alice\tbob", false},
		{"only_tab_rejected", "\t", false},
		{"leading_tab_rejected", "\talice", false},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := metadata.IsValidMetadataText(tc.in)
			if got != tc.want {
				t.Fatalf("IsValidMetadataText(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

// TestValidateGRPCProtoPath exercises the single-source 5-guard validator that
// both governance FMT-37 and contractgen share. Coverage target: all five guard
// branches (empty / no-prefix / control-rune / traversal / subtree-escape) and
// the happy paths including benign intra-subtree "..".
// kernel/ coverage requirement: ≥ 90% (table-driven, per CLAUDE.md).
func TestValidateGRPCProtoPath(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		proto   string
		wantErr string // substring that must appear in the error; "" = expect nil
	}{
		// Happy paths.
		{
			name:  "valid minimal path",
			proto: "contracts/grpc/device/command/v1/device_command.proto",
		},
		{
			name:  "valid nested path",
			proto: "contracts/grpc/access/session/verify/v1/session_verify.proto",
		},
		// Guard 5 benign case: intra-subtree ".." that cleans to a valid path
		// inside contracts/grpc/ must NOT be rejected.
		{
			name:  "intra-subtree dotdot accepted",
			proto: "contracts/grpc/device/../device/x.proto",
			// cleans to "contracts/grpc/device/x.proto" — still inside subtree → nil
		},

		// Guard 1: non-empty.
		{
			name:    "empty proto rejected",
			proto:   "",
			wantErr: "requires proto",
		},

		// Guard 2: must be rooted under contracts/grpc/.
		{
			name:    "wrong prefix rejected",
			proto:   "contracts/http/x.proto",
			wantErr: "must be rooted under",
		},
		{
			name:    "absolute path rejected by prefix check",
			proto:   "/contracts/grpc/x.proto",
			wantErr: "must be rooted under",
		},

		// Guard 3: no control rune.
		{
			name:    "control rune in path rejected",
			proto:   "contracts/grpc/x\n.proto",
			wantErr: "control character",
		},
		{
			name:    "null byte rejected",
			proto:   "contracts/grpc/x\x00.proto",
			wantErr: "control character",
		},

		// Guard 4: filepath.IsLocal (no .. traversal escaping the repo root).
		// Note: "contracts/grpc/../secret.proto" cleans to "contracts/secret.proto"
		// which is still local — IsLocal only rejects paths that escape the root.
		// The path "contracts/grpc/../../../etc/x" cleans to "../etc/x" which IS
		// non-local (escapes repo root).
		{
			name:    "traversal escaping root rejected",
			proto:   "contracts/grpc/../../../etc/x",
			wantErr: "local path",
		},

		// Guard 5: subtree escape after lexical clean. Passes HasPrefix (guard 2)
		// and IsLocal (guard 4), but cleans to contracts/http/x.proto which is
		// OUTSIDE the contracts/grpc/ subtree.
		{
			name:    "cross-subtree dotdot rejected",
			proto:   "contracts/grpc/../http/x.proto",
			wantErr: "escapes the",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := metadata.ValidateGRPCProtoPath(tc.proto)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("ValidateGRPCProtoPath(%q) = %v, want nil", tc.proto, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("ValidateGRPCProtoPath(%q) = nil, want error containing %q", tc.proto, tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("ValidateGRPCProtoPath(%q) = %q, want substring %q", tc.proto, err.Error(), tc.wantErr)
			}
		})
	}
}

// TestGRPCServiceGoName exercises the single-source helper that extracts and
// validates the Go-exported simple name from a proto service FQN.
// kernel/ coverage requirement: ≥ 90% (table-driven, per CLAUDE.md).
func TestGRPCServiceGoName(t *testing.T) {
	t.Parallel()

	cases := []grpcServiceGoNameCase{
		// Valid — exported Go identifiers.
		{
			name:     "FQN exported service",
			service:  "device.command.v1.DeviceCommandService",
			wantName: "DeviceCommandService",
		},
		{
			name:     "unqualified exported name",
			service:  "DeviceCommandService",
			wantName: "DeviceCommandService",
		},
		{
			name:     "short FQN",
			service:  "a.B",
			wantName: "B",
		},

		// Invalid — unexported (lowercase start).
		{
			name:    "lowercase start rejected",
			service: "device.command.v1.fooService",
			wantErr: "not exported",
		},
		{
			name:    "all lowercase rejected",
			service: "foo",
			wantErr: "not exported",
		},

		// Invalid — not a valid identifier.
		{
			name:    "hyphenated name rejected",
			service: "Foo-Bar",
			wantErr: "not a valid Go identifier",
		},

		// Invalid — empty simple name.
		{
			name:    "empty service rejected",
			service: "",
			wantErr: "empty",
		},
		{
			name:    "trailing dot yields empty simple name",
			service: "device.command.v1.",
			wantErr: "empty",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assertGRPCServiceGoName(t, tc)
		})
	}
}

func assertGRPCServiceGoName(t *testing.T, tc grpcServiceGoNameCase) {
	t.Helper()

	got, err := metadata.GRPCServiceGoName(tc.service)
	if tc.wantErr == "" {
		if err != nil {
			t.Fatalf("GRPCServiceGoName(%q) = %v, want nil", tc.service, err)
		}
		if got != tc.wantName {
			t.Fatalf("GRPCServiceGoName(%q) = %q, want %q", tc.service, got, tc.wantName)
		}
		return
	}
	if err == nil {
		t.Fatalf("GRPCServiceGoName(%q) = %q, want error containing %q", tc.service, got, tc.wantErr)
	}
	if !strings.Contains(err.Error(), tc.wantErr) {
		t.Fatalf("GRPCServiceGoName(%q) error = %q, want substring %q", tc.service, err.Error(), tc.wantErr)
	}
}

// TestGRPCProtoRepoRelPath covers module-base prefixing for endpoints.grpc.proto
// resolution. A repo-root contract (File rooted at contracts/grpc/) leaves the
// module-relative proto unchanged; a satellite-module contract (examples/iotdevice)
// prefixes the module base so filesystem readers resolve the real proto location
// (#1151).
func TestGRPCProtoRepoRelPath(t *testing.T) {
	t.Parallel()
	const proto = "contracts/grpc/device/command/v1/device_command.proto"
	cases := []struct {
		name string
		file string
		want string
	}{
		{
			"repo-root contract unchanged",
			"contracts/grpc/device/command/v1/contract.yaml",
			proto,
		},
		{
			"satellite module prefixed",
			"examples/iotdevice/contracts/grpc/device/command/v1/contract.yaml",
			"examples/iotdevice/contracts/grpc/device/command/v1/device_command.proto",
		},
		{
			"nested satellite prefixed",
			"examples/foo/bar/contracts/grpc/x/v1/contract.yaml",
			"examples/foo/bar/" + proto,
		},
		{
			"prefix absent returns proto unchanged",
			"some/other/contract.yaml",
			proto,
		},
		{
			// Documents the "first GRPCProtoPathPrefix segment" semantics: the
			// module base is everything before the FIRST contracts/grpc/. A second
			// occurrence inside the tree does not shift the base. Unreachable in
			// practice (governance FMT-37 / ValidateGRPCProtoPath keep contracts
			// rooted at a single contracts/grpc/), but guards a refactor to
			// strings.LastIndex.
			"duplicate prefix uses first occurrence",
			"examples/iotdevice/contracts/grpc/a/contracts/grpc/b/contract.yaml",
			"examples/iotdevice/" + proto,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := metadata.GRPCProtoRepoRelPath(tc.file, proto); got != tc.want {
				t.Errorf("GRPCProtoRepoRelPath(%q, %q) = %q, want %q", tc.file, proto, got, tc.want)
			}
		})
	}
}
