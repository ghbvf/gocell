// Package migration holds the layer-free database-migration value types shared
// across all GoCell layers. Today that is the Namespace sealed newtype — the
// per-module migration-lineage identity that partitions the goose tracking
// table so multiple Cell modules (the platform plus any external Cell module)
// each own an independent 001..N migration sequence without colliding on a
// single global version space (issue #1089 / root cause R3).
//
// It lives under pkg/ (stdlib only) precisely because every layer must be able
// to reference it: runtime/composition exposes WithMigrations(Namespace, fs.FS)
// for external Cell registration, and adapters/postgres takes Namespace as a
// mandatory typed positional parameter on NewMigrator / VerifyExpectedVersion
// and derives the tracking table schema_migrations_<namespace> from it. Because
// the table is a pure function of a validated Namespace and no public API
// accepts a free tableName string, "track migrations in the bare global
// schema_migrations table" — the R3 collision footgun — is unexpressible.
//
// ref: pkg/tenant.TenantID — sealed-newtype + typed-positional-param pattern.
package migration

import (
	"fmt"
	"regexp"
)

// Namespace identifies an independent migration lineage. Platform migrations
// belong to the reserved PlatformNamespace; each external Cell module declares
// its own. The namespace is used to derive the goose tracking table name
// (schema_migrations_<namespace>) and therefore must be a safe SQL identifier.
//
// Unlike a free string, Namespace is a typed value that production APIs take as
// a mandatory positional parameter: omitting it or passing a bare string is a
// COMPILE error. Validity (non-empty, lowercase identifier, bounded length) is
// a RUNTIME fail-fast invariant checked by ParseNamespace / Validate — an empty
// or malformed string is a legal Go expression, so the type system cannot reject
// it, exactly as pkg/tenant.TenantID documents. The Namespace("x") conversion
// residual is caught at every entry's Validate() guard.
type Namespace string

// PlatformNamespace is the reserved namespace for the platform's own embedded
// migrations (adapters/postgres/migrations). Its tracking table is
// schema_migrations_platform. External Cell modules MUST NOT register under
// this namespace (adapters/postgres.MigrationSet.Add rejects it); only the
// internal platform seed uses it.
const PlatformNamespace Namespace = "platform"

// maxNamespaceLen bounds a namespace so the derived tracking table identifier
// schema_migrations_<namespace> stays within PostgreSQL's 63-byte identifier
// limit. len("schema_migrations_") == 18, leaving 45 bytes for the namespace.
const maxNamespaceLen = 45

// namespaceRe matches a valid namespace: a lowercase identifier starting with a
// letter, followed by lowercase letters, digits, or underscores. This mirrors
// GoCell's no-dash cell/slice id convention and guarantees the value is a safe
// SQL identifier fragment (it cannot inject when concatenated into a table name
// that cannot be parameterised).
var namespaceRe = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// String returns the underlying string.
func (n Namespace) String() string { return string(n) }

// Validate returns nil only for a non-empty, well-formed Namespace: a lowercase
// identifier (^[a-z][a-z0-9_]*$) no longer than maxNamespaceLen. The zero value
// ("") is invalid — a migration set cannot derive a tracking table from an
// absent namespace. Validate asserts the invariant on a value that already has
// the Namespace type (e.g. a Namespace("x") conversion or a struct field),
// catching the conversion-literal escape that the type system cannot.
func (n Namespace) Validate() error {
	return validateNamespaceString(string(n))
}

// ParseNamespace validates s and returns it as a Namespace. It is the sole
// validated constructor for an externally-supplied namespace string (e.g. from
// an external module's registration). Empty or malformed input is an error.
func ParseNamespace(s string) (Namespace, error) {
	if err := validateNamespaceString(s); err != nil {
		return "", err
	}
	return Namespace(s), nil
}

// validateNamespaceString is the single source of truth for the Namespace
// invariant, shared by ParseNamespace and Namespace.Validate.
func validateNamespaceString(s string) error {
	if s == "" {
		return fmt.Errorf("migration: Namespace required (empty)")
	}
	if len(s) > maxNamespaceLen {
		return fmt.Errorf("migration: Namespace %q exceeds %d chars (tracking table "+
			"schema_migrations_<namespace> must fit PostgreSQL's 63-byte identifier limit)", s, maxNamespaceLen)
	}
	if !namespaceRe.MatchString(s) {
		return fmt.Errorf("migration: Namespace %q must match %s (lowercase letter, then "+
			"lowercase letters / digits / underscores)", s, namespaceRe.String())
	}
	return nil
}
