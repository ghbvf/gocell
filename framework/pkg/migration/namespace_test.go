package migration_test

import (
	"strings"
	"testing"

	"github.com/ghbvf/gocell/framework/pkg/migration"
)

func TestParseNamespace(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		in      string
		want    migration.Namespace
		wantErr bool
	}{
		{name: "platform", in: "platform", want: migration.PlatformNamespace},
		{name: "simple", in: "payment", want: "payment"},
		{name: "with_digits", in: "billing2", want: "billing2"},
		{name: "with_underscore", in: "acme_payment", want: "acme_payment"},
		{name: "empty rejected", in: "", wantErr: true},
		{name: "uppercase rejected", in: "Payment", wantErr: true},
		{name: "leading digit rejected", in: "2pay", wantErr: true},
		{name: "leading underscore rejected", in: "_pay", wantErr: true},
		{name: "dash rejected", in: "acme-payment", wantErr: true},
		{name: "dot rejected", in: "acme.payment", wantErr: true},
		{name: "space rejected", in: "acme pay", wantErr: true},
		{name: "too long rejected", in: strings.Repeat("a", 46), wantErr: true},
		{name: "max length ok", in: strings.Repeat("a", 45), want: migration.Namespace(strings.Repeat("a", 45))},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := migration.ParseNamespace(tt.in)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ParseNamespace(%q) = nil error, want error", tt.in)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseNamespace(%q) unexpected error: %v", tt.in, err)
			}
			if got != tt.want {
				t.Fatalf("ParseNamespace(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestNamespaceValidate(t *testing.T) {
	t.Parallel()
	// Zero value is invalid — catches the conversion-literal escape the type
	// system cannot (Namespace("") is a legal Go expression).
	var zero migration.Namespace
	if err := zero.Validate(); err == nil {
		t.Fatal("zero-value Namespace.Validate() = nil, want error")
	}
	if err := migration.Namespace("Bad-NS").Validate(); err == nil {
		t.Fatal("Namespace(\"Bad-NS\").Validate() = nil, want error (conversion-literal escape must be caught)")
	}
	if err := migration.PlatformNamespace.Validate(); err != nil {
		t.Fatalf("PlatformNamespace.Validate() = %v, want nil", err)
	}
}

func TestNamespaceString(t *testing.T) {
	t.Parallel()
	if got := migration.PlatformNamespace.String(); got != "platform" {
		t.Fatalf("PlatformNamespace.String() = %q, want %q", got, "platform")
	}
	ns, err := migration.ParseNamespace("payment")
	if err != nil {
		t.Fatalf("ParseNamespace: %v", err)
	}
	if got := ns.String(); got != "payment" {
		t.Fatalf("String() round-trip = %q, want %q", got, "payment")
	}
}
