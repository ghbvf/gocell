package contractgen

// ContractGenSpec is the top-level template input for one contract.
type ContractGenSpec struct {
	// PackageName is the Go package name derived from the last path segment
	// of PackagePath (e.g. "create", "get", "ordercreated").
	PackageName string
	// PackagePath is the module-relative path for the generated package,
	// e.g. "generated/contracts/http/order/create/v1".
	PackagePath string
	// ContractID is the full contract id, e.g. "http.order.create.v1".
	ContractID string
	// Kind is "http" or "event".
	Kind string
	// SourceFile is the repo-relative path of the contract.yaml that drove
	// generation, e.g. "examples/todoorder/contracts/http/order/create/v1/contract.yaml".
	SourceFile string
	// DTOs holds the flattened list of Go struct definitions (nested types
	// expanded to top-level entries). Template iterates this slice directly.
	DTOs []DTOSpec
	// Endpoint is non-nil when Kind == "http".
	Endpoint *HTTPEndpointSpec
	// Event is non-nil when Kind == "event".
	Event *EventEndpointSpec
}

// DTOSpec is one Go struct definition.
// Nested is used only as an intermediate representation during builder
// flattening; by the time ContractGenSpec.DTOs is populated, all nested
// types have been promoted to top-level and Nested is empty.
type DTOSpec struct {
	// Name is the PascalCase struct name, e.g. Request, Response, Payload.
	Name string
	// Doc is the human-readable description (from schema.title).
	Doc string
	// Fields lists the struct fields in source-declared order.
	Fields []DTOField
	// Nested holds intermediate nested-object types discovered during schema
	// traversal. Callers of BuildContractSpec see an empty slice — the builder
	// promotes nested types to ContractGenSpec.DTOs and clears this field.
	Nested []DTOSpec
}

// DTOField describes a single struct field.
type DTOField struct {
	// Name is the PascalCase Go field name.
	Name string
	// JSONTag is the JSON tag value, e.g. "item,omitempty".
	JSONTag string
	// GoType is the Go type expression, e.g. "string", "int64", "*ResponseData".
	GoType string
	// Required indicates whether the field is in the schema's required list.
	Required bool
	// Doc is an optional comment, used for format hints (uuid, date-time).
	Doc string
}

// HTTPEndpointSpec holds HTTP-specific endpoint information.
type HTTPEndpointSpec struct {
	// Method is the HTTP method in upper-case, e.g. "POST".
	Method string
	// Path is the full URL path including chi-style placeholders, e.g. "/api/v1/orders/{id}".
	Path string
	// PathParams lists parameters embedded in the URL path, in declaration order.
	PathParams []ParamSpec
	// QueryParams lists URL query parameters, in declaration order.
	QueryParams []ParamSpec
	// SuccessCode is the HTTP status code on success, e.g. 201 or 200.
	SuccessCode int
	// NoContent indicates a 204 No Content response (no body written).
	NoContent bool
	// HandlerMethod is the PascalCase method name on the Service interface,
	// derived from the last domain segment, e.g. "Create", "Get", "List".
	HandlerMethod string
	// HasBody is true when Method is POST, PUT, or PATCH.
	HasBody bool
}

// EventEndpointSpec holds event-specific endpoint information.
type EventEndpointSpec struct {
	// Topic is the broker topic name (contract id with version suffix stripped),
	// e.g. "event.order-created".
	Topic string
	// HandlerMethod is the PascalCase handler method name,
	// e.g. "HandleOrderCreated".
	HandlerMethod string
	// Replayable indicates whether this event supports replay.
	Replayable bool
	// DeliverySemantics is the declared delivery guarantee, e.g. "at-least-once".
	DeliverySemantics string
}

// ParamSpec describes a single HTTP path or query parameter.
type ParamSpec struct {
	// Name is the parameter name as declared in contract.yaml, e.g. "id", "cursor".
	Name string
	// GoName is the PascalCase Go field name used in the Request struct.
	GoName string
	// GoType is the Go scalar type: "string", "int64", "float64", or "bool".
	GoType string
	// Required is true for path params (always) or explicitly-required query params.
	Required bool
	// Doc is an optional hint comment.
	Doc string
	// MinLength applies to string params.
	MinLength *int
	// MaxLength applies to string params.
	MaxLength *int
	// Minimum applies to numeric params.
	Minimum *int64
	// Maximum applies to numeric params.
	Maximum *int64
}
