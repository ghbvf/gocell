package contractspec

import (
	"fmt"
	"strings"

	"github.com/ghbvf/gocell/kernel/cellvocab"
	"github.com/ghbvf/gocell/kernel/metadata"
)

// ContractSpec is the runtime descriptor for one contract endpoint.
// It is consumed by:
//   - runtime/auth.Mount (HTTP route binding)
//   - runtime/eventbus / kernel/cell.Registrar.Subscribe (event subscription)
//   - tracing span attributes (gocell.contract.id / kind / transport)
//
// The zero value is invalid — callers must populate ID / Kind / Transport
// and the kind-specific fields, then rely on auth.Mount / wrapper.HTTPHandler
// to Validate() before registration.
//
// Construction-site catalog and archtest enforcement gates are documented
// in this package's doc.go (single source). Composite literal under cells/,
// examples/*/cells/, and runtime/ is forbidden by archtest
// NO-MANUAL-CONTRACTSPEC-LITERAL-01.
//
// ref: k8s.io/apimachinery — lightweight value types shared across layers
// without runtime parsing dependencies.
type ContractSpec struct {
	// ID is the contract identifier, e.g. "http.auth.login.v1" or
	// "event.session.revoked.v1". It MUST match the id field in the
	// contract.yaml file identified by Kind + path.
	ID string

	// Kind is one of ContractHTTP | ContractEvent | ContractCommand |
	// ContractProjection | ContractGRPC | ContractSaga.
	Kind cellvocab.ContractKind

	// Transport names the wire protocol: "http" for Kind=="http", "grpc" for
	// Kind=="grpc", "amqp" / "internal" / ... for event/command/projection.
	Transport string

	// HTTP-specific fields; required when Kind == "http", rejected otherwise.
	Method string // upper-case HTTP verb
	Path   string // path template, e.g. "/api/v1/auth/login"

	// Event-specific fields; required when Kind == "event", rejected
	// otherwise. Topic is the broker destination name.
	Topic string

	// Clients is the allowlist of caller cell IDs for internal HTTP endpoints.
	// Required when Kind=="http" and Path has prefix "/internal/v1/"; must be
	// empty for non-internal paths. The list is mirrored in contract.yaml
	// endpoints.clients and enforced at runtime by auth.RequireCallerCell.
	Clients []string

	// GRPC carries the grpc transport details; required (non-nil) when
	// Kind == "grpc", rejected otherwise.
	GRPC *GRPCEndpointSpec
}

// GRPCEndpointSpec is the grpc-kind transport descriptor carried by
// ContractSpec.GRPC. It mirrors metadata.GRPCTransportMeta but is the runtime
// value type (kernel/contractspec is dependency-free of metadata parsing at the
// registration boundary). Service + Method are the wire identity; ProtoPackage
// is the proto file's `package` declaration (populated by codegen in PR 6).
type GRPCEndpointSpec struct {
	Service       string // proto FQ service name, e.g. "device.command.v1.DeviceCommandService"
	Method        string // proto method name, e.g. "IssueCommand"
	StreamingType string // unary | server-stream | client-stream | bidi (empty → unary)
	Proto         string // contracts-relative .proto path
	// ProtoPackage is the proto `package` declaration (e.g. "device.command.v1").
	// Populated by codegen in PR 6; empty in PR 1–5 and must not be relied upon.
	ProtoPackage string
}

// Validate returns an error if the spec is malformed. Validation is separate
// from construction so test fixtures can assert negative cases without the
// cost of a full wrapper.HTTPHandler call.
func (s ContractSpec) Validate() error {
	if strings.TrimSpace(s.ID) == "" {
		return fmt.Errorf("contractspec.ContractSpec: ID must not be empty")
	}
	if strings.TrimSpace(string(s.Kind)) == "" {
		return fmt.Errorf("contractspec.ContractSpec: Kind must not be empty")
	}
	if strings.TrimSpace(s.Transport) == "" {
		return fmt.Errorf("contractspec.ContractSpec: Transport must not be empty")
	}

	// Cross-field exclusivity for the grpc transport block: the GRPC subtree is
	// the only field group introduced after the original http/event split, so
	// the foreign-block rejection lives here at the dispatch point (one guard
	// covers http, event, command, projection, and unknown kinds) rather than
	// being duplicated into each non-grpc validator. validateGRPC itself rejects
	// the inverse leakage (http Method/Path or event Topic on a grpc spec).
	if s.Kind != cellvocab.ContractGRPC && s.GRPC != nil {
		return fmt.Errorf("contractspec.ContractSpec[%s]: %s kind must not carry a GRPC block", s.ID, s.Kind)
	}

	switch s.Kind {
	case cellvocab.ContractHTTP:
		return s.validateHTTP()
	case cellvocab.ContractEvent:
		return s.validateEvent()
	case cellvocab.ContractGRPC:
		return s.validateGRPC()
	case cellvocab.ContractCommand, cellvocab.ContractProjection, cellvocab.ContractSaga:
		// Allowed but no additional kind-specific validation beyond the
		// kind-agnostic ID/Kind/Transport checks above — command/projection
		// transports are future PRs; saga is orchestrated via the runtime
		// Coordinator (kernel/saga), not a wire transport.
		return nil
	default:
		return fmt.Errorf("contractspec.ContractSpec: Kind %q not recognized (http|event|command|projection|grpc|saga)", s.Kind)
	}
}

func (s ContractSpec) validateHTTP() error {
	if s.Method == "" {
		return fmt.Errorf("contractspec.ContractSpec[%s]: http kind requires Method", s.ID)
	}
	if s.Method != strings.ToUpper(s.Method) {
		return fmt.Errorf("contractspec.ContractSpec[%s]: Method %q must be upper-case", s.ID, s.Method)
	}
	if s.Path == "" {
		return fmt.Errorf("contractspec.ContractSpec[%s]: http kind requires Path", s.ID)
	}
	if !strings.HasPrefix(s.Path, "/") {
		return fmt.Errorf("contractspec.ContractSpec[%s]: Path %q must start with '/'", s.ID, s.Path)
	}
	if s.Topic != "" {
		return fmt.Errorf("contractspec.ContractSpec[%s]: http kind must not carry Topic", s.ID)
	}
	isInternalPath := strings.HasPrefix(s.Path, cellvocab.InternalPathPrefix) ||
		s.Path == strings.TrimSuffix(cellvocab.InternalPathPrefix, "/")
	if isInternalPath && len(s.Clients) == 0 {
		return fmt.Errorf(
			"ContractSpec[%s]: internal path requires non-empty Clients "+
				"(declare in contract.yaml endpoints.clients and mirror in literal) "+
				"(see contracts/http/config/internal/get/v1/contract.yaml for an example)",
			s.ID)
	}
	if !isInternalPath && len(s.Clients) > 0 {
		return fmt.Errorf("ContractSpec[%s]: non-internal path must not declare Clients", s.ID)
	}
	for i, c := range s.Clients {
		if !metadata.MatchCellID(c) {
			return fmt.Errorf("ContractSpec[%s]: Clients[%d] %q does not match cell ID pattern %s",
				s.ID, i, c, metadata.CellIDPattern)
		}
	}
	return nil
}

func (s ContractSpec) validateEvent() error {
	if s.Topic == "" {
		return fmt.Errorf("contractspec.ContractSpec[%s]: event kind requires Topic", s.ID)
	}
	if s.Method != "" || s.Path != "" {
		return fmt.Errorf("contractspec.ContractSpec[%s]: event kind must not carry Method/Path", s.ID)
	}
	return nil
}

func (s ContractSpec) validateGRPC() error {
	if s.GRPC == nil {
		return fmt.Errorf("contractspec.ContractSpec[%s]: grpc kind requires GRPC block", s.ID)
	}
	if s.GRPC.Service == "" {
		return fmt.Errorf("contractspec.ContractSpec[%s]: grpc kind requires Service", s.ID)
	}
	if s.GRPC.Method == "" {
		return fmt.Errorf("contractspec.ContractSpec[%s]: grpc kind requires Method", s.ID)
	}
	if s.GRPC.Proto == "" {
		return fmt.Errorf("contractspec.ContractSpec[%s]: grpc kind requires Proto", s.ID)
	}
	if !strings.HasPrefix(s.GRPC.Proto, metadata.GRPCProtoPathPrefix) {
		return fmt.Errorf("contractspec.ContractSpec[%s]: grpc Proto %q must be rooted under %q",
			s.ID, s.GRPC.Proto, metadata.GRPCProtoPathPrefix)
	}
	// StreamingType is optional; empty is the unary default. When present it must
	// be one of metadata.GRPCStreamingTypeEnum — the same single source the
	// contract.schema.json enum and governance FMT-37 consume, so the three
	// validation surfaces never disagree.
	if s.GRPC.StreamingType != "" && !metadata.IsKnownGRPCStreamingType(s.GRPC.StreamingType) {
		return fmt.Errorf(
			"contractspec.ContractSpec[%s]: grpc StreamingType %q not recognized (unary|server-stream|client-stream|bidi, or omit for unary)",
			s.ID, s.GRPC.StreamingType)
	}
	if s.Method != "" || s.Path != "" || s.Topic != "" {
		return fmt.Errorf("contractspec.ContractSpec[%s]: grpc kind must not carry http Method/Path or event Topic", s.ID)
	}
	return nil
}

// GRPCInfo returns the grpc transport descriptor for a grpc-kind spec, or nil
// for any other kind. Callers use it to read service/method without a type
// switch on Kind.
func (s ContractSpec) GRPCInfo() *GRPCEndpointSpec {
	if s.Kind != cellvocab.ContractGRPC {
		return nil
	}
	return s.GRPC
}
