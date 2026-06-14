package metadata

import "regexp"

// ownershipPathRe matches valid ownership DSL path expressions.
// DSL: prefix must be ctx or path; followed by one or more dot-separated
// segments each starting with a lowercase letter followed by zero or more
// alphanumeric characters (camelCase). The segment first character being
// lowercase enforces camelCase and rejects snake_case and PascalCase forms.
//
// Compiled once at package init to avoid repeated allocation in hot paths.
var ownershipPathRe = regexp.MustCompile(`^(ctx|path)\.[a-z][a-zA-Z0-9]*(\.[a-z][a-zA-Z0-9]*)*$`)

// OwnershipDeclarationRequired reports whether the auth configuration mandates
// an ownership block declaration. Single-source predicate shared by kernel/governance
// FMT-32 and schema/metadata validation.
//
// The ownership block is required when auth.ServiceOwned is true, regardless of
// other flags (e.g. PasswordResetExempt may coexist with ServiceOwned per FMT-27).
func OwnershipDeclarationRequired(auth HTTPAuthMeta) bool { return auth.ServiceOwned }

// OwnershipPathValid reports whether the ownership path expression conforms to
// the DSL shape. Single-source predicate shared by kernel/governance FMT-32
// and schema/metadata validation.
//
// DSL: prefix is ctx or path, followed by one or more dot-separated segments
// where each segment starts with a lowercase letter (camelCase enforced; snake_case
// and PascalCase are rejected). path.<param> referential integrity (the param must
// be declared in the route's pathParams) is a governance-layer concern and is not
// checked here.
//
// Single-segment forms:
//   - "path.<param>" alone is valid — the path param value itself is the owner
//     key (e.g. DELETE /users/{id} where {id} directly identifies the owned
//     resource). "path.<param>.<field>" selects an owner field on the located
//     resource (e.g. "path.id.userID" fetches the userID field of the session
//     record at path param "id").
//   - "ctx.<seg>" alone is valid — the caller context field itself is the owner
//     key (e.g. "ctx.userID").
//
// Unicode chars are rejected by the ASCII character class — intentional
// fail-closed. No length cap is enforced by this predicate; governance callers
// reject absurd lengths if needed.
func OwnershipPathValid(expr string) bool {
	return ownershipPathRe.MatchString(expr)
}
