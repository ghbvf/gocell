package contractspec

import (
	"fmt"
	"strings"

	"github.com/ghbvf/gocell/kernel/cellvocab"
)

// ContractSpec is the runtime descriptor for one contract endpoint.
// It is consumed by:
//   - runtime/auth.Mount (HTTP route binding)
//   - runtime/eventbus / kernel/cell.Registry.Subscribe (event subscription)
//   - tracing span attributes (gocell.contract.id / kind / transport)
//
// Cells MUST NOT construct ContractSpec literals. The only valid construction
// site is generated/contracts/**/spec_gen.go (private `var spec`).
// Subscription/route mounting goes through the generated NewSubscription /
// NewHandler adapters in generated/contracts/**.
//
// Three archtest gates enforce this invariant:
//   - CELLS-NO-CONTRACTSPEC-IMPORT-01
//   - NO-MANUAL-CONTRACTSPEC-LITERAL-01
//   - EVENT-SUBSCRIPTION-CONTRACTGEN-COVERAGE-01
//
// The zero value is invalid — callers must populate ID / Kind / Transport
// and the kind-specific fields, then rely on auth.Mount / wrapper.HTTPHandler
// to Validate() before registration.
//
// ref: k8s.io/apimachinery — lightweight value types shared across layers
// without runtime parsing dependencies.
type ContractSpec struct {
	// ID is the contract identifier, e.g. "http.auth.login.v1" or
	// "event.session.revoked.v1". It MUST match the id field in the
	// contract.yaml file identified by Kind + path.
	ID string

	// Kind is one of ContractHTTP | ContractEvent | ContractCommand | ContractProjection.
	Kind cellvocab.ContractKind

	// Transport names the wire protocol: "http" for Kind=="http",
	// "amqp" / "internal" / ... for event/command/projection.
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

	switch s.Kind {
	case cellvocab.ContractHTTP:
		return s.validateHTTP()
	case cellvocab.ContractEvent:
		return s.validateEvent()
	case cellvocab.ContractCommand, cellvocab.ContractProjection:
		// Allowed but no additional validation yet — future PRs add
		// command/projection transports.
		return nil
	default:
		return fmt.Errorf("contractspec.ContractSpec: Kind %q not recognized (http|event|command|projection)", s.Kind)
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
	isInternalPath := strings.HasPrefix(s.Path, cellvocab.InternalPathPrefix) || s.Path == strings.TrimSuffix(cellvocab.InternalPathPrefix, "/")
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
		if !isCellIDLike(c) {
			return fmt.Errorf("ContractSpec[%s]: Clients[%d] %q does not match cell ID pattern ^[a-z][a-z0-9-]*$",
				s.ID, i, c)
		}
	}
	return nil
}

// isCellIDLike reports whether s matches the cell-ID pattern
// `^[a-z][a-z0-9-]*$`. Implemented byte-wise so that kernel/contractspec
// avoids a package-level regexp var (FMT-19 forbids initializer state in
// kernel/). Mirrors runtime/auth.callerCellPattern semantics but adds no
// runtime dependency.
func isCellIDLike(s string) bool {
	if s == "" {
		return false
	}
	if s[0] < 'a' || s[0] > 'z' {
		return false
	}
	for i := 1; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z':
		case c >= '0' && c <= '9':
		case c == '-':
		default:
			return false
		}
	}
	return true
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
