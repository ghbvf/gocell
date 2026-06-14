package tenant_test

import (
	"context"
	"errors"
	"testing"

	"github.com/ghbvf/gocell/framework/pkg/ctxkeys"
	"github.com/ghbvf/gocell/framework/pkg/tenant"
)

const (
	canonicalTID = "3f259e1c-1c4a-4b6e-9c2f-2d3a4b5c6d7e"
	upperTID     = "3F259E1C-1C4A-4B6E-9C2F-2D3A4B5C6D7E"
)

func TestFromContext(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		ctx     context.Context
		want    tenant.TenantID
		wantErr bool
	}{
		{
			name: "canonical value returns TenantID",
			ctx:  ctxkeys.WithTenantID(context.Background(), canonicalTID),
			want: tenant.TenantID(canonicalTID),
		},
		{
			name: "uppercase value is canonicalized",
			ctx:  ctxkeys.WithTenantID(context.Background(), upperTID),
			want: tenant.TenantID(canonicalTID),
		},
		{
			name:    "missing key fails closed",
			ctx:     context.Background(),
			wantErr: true,
		},
		{
			name:    "empty value fails closed",
			ctx:     ctxkeys.WithTenantID(context.Background(), ""),
			wantErr: true,
		},
		{
			name:    "non-uuid value fails closed",
			ctx:     ctxkeys.WithTenantID(context.Background(), "not-a-uuid"),
			wantErr: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := tenant.FromContext(tc.ctx)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("FromContext() expected error, got nil (value %q)", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("FromContext() unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("FromContext() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestFromContextErrorIsNotSentinel(t *testing.T) {
	t.Parallel()
	// FromContext returns plain wrapped errors; callers map to HTTP. Guard that
	// a missing tenant does not accidentally surface as a nil error.
	_, err := tenant.FromContext(context.Background())
	if err == nil || errors.Is(err, context.Canceled) {
		t.Fatalf("expected a non-nil, non-context error, got %v", err)
	}
}
