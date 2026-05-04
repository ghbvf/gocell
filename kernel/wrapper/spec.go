package wrapper

import (
	"fmt"
	"strings"
)

// ContractSpec is the minimal subset of a contract definition that wrapper
// needs to emit observability annotations. Cells construct the literal
// inline next to each handler (one per HTTP route or outbox subscription)
// and pass it into auth.Mount / eventrouter.Subscribe; governance rule
// FMT-17 cross-references the Go literals against contracts/**.yaml at
// validate time, so the duplication is caught statically.
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

	// Kind is one of "http" | "event" | "command" | "projection".
	Kind string

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
		return fmt.Errorf("wrapper.ContractSpec: ID must not be empty")
	}
	if strings.TrimSpace(s.Kind) == "" {
		return fmt.Errorf("wrapper.ContractSpec: Kind must not be empty")
	}
	if strings.TrimSpace(s.Transport) == "" {
		return fmt.Errorf("wrapper.ContractSpec: Transport must not be empty")
	}

	switch s.Kind {
	case "http":
		return s.validateHTTP()
	case "event":
		return s.validateEvent()
	case "command", "projection":
		// Allowed but no additional validation yet — future PRs add
		// command/projection transports.
		return nil
	default:
		return fmt.Errorf("wrapper.ContractSpec: Kind %q not recognized (http|event|command|projection)", s.Kind)
	}
}

func (s ContractSpec) validateHTTP() error {
	if s.Method == "" {
		return fmt.Errorf("wrapper.ContractSpec[%s]: http kind requires Method", s.ID)
	}
	if s.Method != strings.ToUpper(s.Method) {
		return fmt.Errorf("wrapper.ContractSpec[%s]: Method %q must be upper-case", s.ID, s.Method)
	}
	if s.Path == "" {
		return fmt.Errorf("wrapper.ContractSpec[%s]: http kind requires Path", s.ID)
	}
	if !strings.HasPrefix(s.Path, "/") {
		return fmt.Errorf("wrapper.ContractSpec[%s]: Path %q must start with '/'", s.ID, s.Path)
	}
	if s.Topic != "" {
		return fmt.Errorf("wrapper.ContractSpec[%s]: http kind must not carry Topic", s.ID)
	}
	isInternalPath := strings.HasPrefix(s.Path, "/internal/v1/") || s.Path == "/internal/v1"
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
		if !isCellIDLike(strings.ToLower(c)) {
			return fmt.Errorf("ContractSpec[%s]: Clients[%d] %q does not match cell ID pattern ^[a-z][a-z0-9-]*$",
				s.ID, i, c)
		}
	}
	return nil
}

// isCellIDLike reports whether s matches the cell-ID pattern
// `^[a-z][a-z0-9-]*$`. Implemented byte-wise so that kernel/wrapper avoids a
// package-level regexp var (FMT-19 forbids initializer state in kernel/).
// Mirrors runtime/auth.callerCellPattern semantics but adds no runtime
// dependency.
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
		return fmt.Errorf("wrapper.ContractSpec[%s]: event kind requires Topic", s.ID)
	}
	if s.Method != "" || s.Path != "" {
		return fmt.Errorf("wrapper.ContractSpec[%s]: event kind must not carry Method/Path", s.ID)
	}
	return nil
}

// EventSpec returns a ContractSpec for a broker-backed event contract whose
// Topic equals its ID — the common case across accesscore / auditcore /
// configcore event subscriptions. Callers that need a Topic different from
// the contract id (e.g. subscribing to a wildcard routing key) should keep
// constructing the literal explicitly.
//
// Note for governance: FMT-18 (PR-A11 round-4 + PR246-FU1) parses both
// wrapper.ContractSpec{...} composite literals AND wrapper.EventSpec(...)
// call expressions via go/parser, so specs built via EventSpec are still
// cross-checked against contracts/**/contract.yaml. FMT-18 is strict-only
// — it runs under `gocell validate --strict` (and CI's strict job); a
// plain `gocell validate` does not exercise the cross-check. The id
// argument must be a string literal (or a constant whose value the AST
// can resolve) so the validator can look up `contracts/**/contract.yaml`
// by id and verify Kind / Method / Path agreement; otherwise FMT-18 emits
// a WARNING and asks the author to inline the literal. Prefer EventSpec
// when the id==topic identity is genuine and stable; the literal form
// remains valid when Topic must diverge from ID.
func EventSpec(id, transport string) ContractSpec {
	return ContractSpec{
		ID:        id,
		Kind:      "event",
		Transport: transport,
		Topic:     id,
	}
}
