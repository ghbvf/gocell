// Package command wires kernel command workers (queue discovery + dispatch
// registry) into the runtime. The periodic command-expiry sweep is no longer a
// runtime control shell here — the kernel Sweeper implements reconcile.Reconciler
// and is driven by a kernel/reconcile.Loop at the cell (see
// examples/iotdevice/cells/devicecell).
package command

import (
	"sync"

	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/pkg/idutil"
	"github.com/ghbvf/gocell/framework/pkg/validation"
)

// CommandID is the registry key — the command contract id (e.g.
// "command.devicecommand.enqueue.v1").
//
// It is a type alias (not a defined type) for idutil.SafeID so that:
//   - idutil.SafeID.Validate() is available for id validation without conversion.
//   - Generated code can declare `const DispatchID idutil.SafeID = "..."` and
//     pass it directly to RegisterHandler/LookupHandler without a conversion
//     expression (优雅简洁 — the SafeID.Validate guard is the real key guard).
//
// Because it is an alias, CommandID and idutil.SafeID are interchangeable at the
// type level — the type system does NOT distinguish "a command registry key" from
// any other SafeID. The funnel boundary (only generated code registers/dispatches)
// is enforced by archtest COMMAND-DISPATCH-REGISTER-CALLER-01, not by the type.
type CommandID = idutil.SafeID

// Registry is the sealed in-process command dispatcher core. It maps each
// CommandID to exactly one handler (boxed as any) so that the generated
// command_gen.go Dispatch functions can perform type-safe lookups at runtime.
//
// Sealed-construction rationale: mu and handlers are unexported, so business
// packages holding a *Registry can mutate it only via RegisterHandler and read
// it only via LookupHandler. Both methods are intended to be called exclusively
// from generated command code; the caller-allowlist archtest
// COMMAND-DISPATCH-REGISTER-CALLER-01 enforces that at the call-site level.
//
// The registry stores handlers boxed as any because Go cannot express a
// heterogeneous map keyed by string with value type "one of many generated
// Handler interfaces" in the type system — this is the same posture used by
// the saga *Definition registry and Watermill's command-processor map. The
// generated Dispatch function performs the type-assert immediately after
// LookupHandler, keeping the unsafe boundary as narrow as possible.
//
// Thread safety: all operations are protected by an internal RWMutex.
// RegisterHandler uses a full write-lock; LookupHandler uses a read-lock.
type Registry struct {
	mu       sync.RWMutex
	handlers map[CommandID]any
}

// NewRegistry returns a new, empty Registry ready for use.
func NewRegistry() *Registry {
	return &Registry{
		handlers: make(map[CommandID]any),
	}
}

// RegisterHandler stores handler under id. Returns an error for any of the
// following conditions:
//
//   - id is empty or contains unsafe characters (idutil.SafeID.Validate fails):
//     KindInvalid / ErrValidationFailed.
//   - handler is nil or typed-nil (validation.IsNilInterface):
//     KindInvalid / ErrValidationFailed.
//   - id is already registered (one-to-one command→handler mapping, mirrors
//     Watermill DuplicateCommandHandlerError and the saga registry duplicate
//     check): KindConflict / ErrConflict.
//
// Thread-safe (acquires mu.Lock).
func (r *Registry) RegisterHandler(id CommandID, handler any) error {
	// The `id == ""` check is NOT redundant with Validate(): idutil.SafeID treats
	// the empty string as VALID (zero/absent semantic), but an empty command id is
	// a registration bug here — every command has a non-empty DispatchID. Do not
	// "simplify" this guard away.
	if err := id.Validate(); err != nil || id == "" {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"command registry: invalid command id",
			errcode.WithDetails(errcode.PublicString("commandId", string(id))))
	}
	if validation.IsNilInterface(handler) {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"command registry: handler must not be nil",
			errcode.WithDetails(errcode.PublicString("commandId", string(id))))
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.handlers[id]; exists {
		return errcode.New(errcode.KindConflict, errcode.ErrConflict,
			"command registry: handler already registered for command id",
			errcode.WithDetails(errcode.PublicString("commandId", string(id))))
	}

	r.handlers[id] = handler
	return nil
}

// LookupHandler returns the boxed handler for id. If id is not registered,
// it returns (nil, false). Thread-safe (acquires mu.RLock).
func (r *Registry) LookupHandler(id CommandID) (any, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	h, ok := r.handlers[id]
	return h, ok
}
