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
		return fmt.Errorf("wrapper.ContractSpec: Kind %q not recognised (http|event|command|projection)", s.Kind)
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
	return nil
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
