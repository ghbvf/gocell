package outbox

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/pkg/ctxkeys"
	"github.com/ghbvf/gocell/pkg/idutil"
)

// ---------------------------------------------------------------------------
// PrincipalMetadata.IsZero
// ---------------------------------------------------------------------------

func TestPrincipalMetadata_IsZero(t *testing.T) {
	t.Run("empty struct is zero", func(t *testing.T) {
		assert.True(t, PrincipalMetadata{}.IsZero())
	})
	t.Run("ActorID non-empty is not zero", func(t *testing.T) {
		assert.False(t, PrincipalMetadata{ActorID: "actor-1"}.IsZero())
	})
	t.Run("SubjectID non-empty is not zero", func(t *testing.T) {
		assert.False(t, PrincipalMetadata{SubjectID: "subj-1"}.IsZero())
	})
	t.Run("TenantID non-empty is not zero", func(t *testing.T) {
		assert.False(t, PrincipalMetadata{TenantID: "tenant-1"}.IsZero())
	})
	t.Run("SessionID non-empty is not zero", func(t *testing.T) {
		assert.False(t, PrincipalMetadata{SessionID: "sess-1"}.IsZero())
	})
}

// TestPrincipalMetadata_IsZero_FieldCoverageInvariant uses reflection to
// assert IsZero examines every exported field. Adding a new field without
// extending IsZero will fail this test (mirror of
// TestObservabilityMetadata_IsZero_FieldCoverageInvariant).
func TestPrincipalMetadata_IsZero_FieldCoverageInvariant(t *testing.T) {
	typ := reflect.TypeFor[PrincipalMetadata]()
	require.Equal(t, reflect.Struct, typ.Kind())
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		if !f.IsExported() {
			continue
		}
		t.Run(f.Name, func(t *testing.T) {
			v := reflect.New(typ).Elem()
			fv := v.Field(i)
			require.Truef(t, fv.CanSet(), "field %s must be settable for the invariant check", f.Name)
			switch f.Type.Kind() {
			case reflect.String:
				fv.SetString("non-zero")
			default:
				t.Skipf("PrincipalMetadata.%s is not a string — extend the invariant test for new field types", f.Name)
			}
			p := v.Interface().(PrincipalMetadata)
			assert.Falsef(t, p.IsZero(),
				"setting %s to non-empty must make IsZero() return false; "+
					"if you added a new field, extend PrincipalMetadata.IsZero accordingly",
				f.Name)
		})
	}
}

// ---------------------------------------------------------------------------
// PrincipalMetadata.Validate (SafeID per-field guards)
// ---------------------------------------------------------------------------

func TestPrincipalMetadata_Validate(t *testing.T) {
	tooLong := idutil.SafeID(strings.Repeat("a", idutil.MaxMetadataIDLen+1))

	cases := []struct {
		name      string
		p         PrincipalMetadata
		wantError string // empty = expect nil
	}{
		{name: "zero is valid", p: PrincipalMetadata{}},
		{name: "all fields valid", p: PrincipalMetadata{
			ActorID: "actor-1", SubjectID: "subj-1", TenantID: "tenant-1", SessionID: "sess-1",
		}},
		{name: "ActorID too long", p: PrincipalMetadata{ActorID: tooLong}, wantError: "too long"},
		{name: "SubjectID too long", p: PrincipalMetadata{SubjectID: tooLong}, wantError: "too long"},
		{name: "TenantID too long", p: PrincipalMetadata{TenantID: tooLong}, wantError: "too long"},
		{name: "SessionID too long", p: PrincipalMetadata{SessionID: tooLong}, wantError: "too long"},
		{name: "ActorID unsafe chars", p: PrincipalMetadata{ActorID: "actor; DROP TABLE"}, wantError: "unsafe characters"},
		{name: "SubjectID newline injection", p: PrincipalMetadata{SubjectID: "subj\n"}, wantError: "unsafe characters"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.p.Validate()
			if tc.wantError == "" {
				assert.NoError(t, err)
			} else {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantError)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// ContextPrincipal / RestoreToContext round-trip
// ---------------------------------------------------------------------------

func TestContextPrincipal(t *testing.T) {
	t.Run("empty ctx returns empty principal", func(t *testing.T) {
		p := ContextPrincipal(context.Background())
		assert.True(t, p.IsZero())
	})
	t.Run("populated ctx returns populated principal", func(t *testing.T) {
		ctx := context.Background()
		ctx = ctxkeys.WithActor(ctx, "actor-1")
		ctx = ctxkeys.WithSubject(ctx, "subj-1")
		ctx = ctxkeys.WithTenant(ctx, "tenant-1")
		ctx = ctxkeys.WithSession(ctx, "sess-1")

		p := ContextPrincipal(ctx)
		assert.Equal(t, idutil.SafeID("actor-1"), p.ActorID)
		assert.Equal(t, idutil.SafeID("subj-1"), p.SubjectID)
		assert.Equal(t, idutil.SafeID("tenant-1"), p.TenantID)
		assert.Equal(t, idutil.SafeID("sess-1"), p.SessionID)
	})
}

func TestPrincipalMetadata_RestoreToContext(t *testing.T) {
	t.Run("empty principal is no-op", func(t *testing.T) {
		ctx := PrincipalMetadata{}.RestoreToContext(context.Background())
		_, ok := ctxkeys.ActorFrom(ctx)
		assert.False(t, ok)
	})
	t.Run("populates all four context keys", func(t *testing.T) {
		p := PrincipalMetadata{
			ActorID: "actor-1", SubjectID: "subj-1",
			TenantID: "tenant-1", SessionID: "sess-1",
		}
		ctx := p.RestoreToContext(context.Background())

		got, ok := ctxkeys.ActorFrom(ctx)
		require.True(t, ok)
		assert.Equal(t, "actor-1", got)

		got, ok = ctxkeys.SubjectFrom(ctx)
		require.True(t, ok)
		assert.Equal(t, "subj-1", got)

		got, ok = ctxkeys.TenantFrom(ctx)
		require.True(t, ok)
		assert.Equal(t, "tenant-1", got)

		got, ok = ctxkeys.SessionFrom(ctx)
		require.True(t, ok)
		assert.Equal(t, "sess-1", got)
	})
	t.Run("existing ctx values win (idempotent restore)", func(t *testing.T) {
		// Mirror ObservabilityMetadata.RestoreToContext semantics — consumer
		// ctx may legitimately carry its own principal from an outer
		// middleware; the entry's identity is a fallback, not a mandate.
		ctx := context.Background()
		ctx = ctxkeys.WithActor(ctx, "existing-actor")
		ctx = PrincipalMetadata{ActorID: "entry-actor"}.RestoreToContext(ctx)
		got, _ := ctxkeys.ActorFrom(ctx)
		assert.Equal(t, "existing-actor", got, "existing ctx value MUST win")
	})
	t.Run("unsafe values silently dropped", func(t *testing.T) {
		p := PrincipalMetadata{ActorID: idutil.SafeID("actor\nevil")}
		ctx := p.RestoreToContext(context.Background())
		_, ok := ctxkeys.ActorFrom(ctx)
		assert.False(t, ok, "unsafe SafeID must not be restored to ctx")
	})
}

// ---------------------------------------------------------------------------
// Entry.InjectPrincipalFromContext (sealed write path)
// ---------------------------------------------------------------------------

func TestEntry_InjectPrincipalFromContext(t *testing.T) {
	ctx := context.Background()
	ctx = ctxkeys.WithActor(ctx, "actor-x")
	ctx = ctxkeys.WithSubject(ctx, "subj-x")
	ctx = ctxkeys.WithTenant(ctx, "tenant-x")
	ctx = ctxkeys.WithSession(ctx, "sess-x")

	e := &Entry{ID: "evt-1"}
	e.InjectPrincipalFromContext(ctx)

	assert.Equal(t, idutil.SafeID("actor-x"), e.Principal.ActorID)
	assert.Equal(t, idutil.SafeID("subj-x"), e.Principal.SubjectID)
	assert.Equal(t, idutil.SafeID("tenant-x"), e.Principal.TenantID)
	assert.Equal(t, idutil.SafeID("sess-x"), e.Principal.SessionID)
}

func TestEntry_InjectPrincipalFromContext_Idempotent_Overwrite(t *testing.T) {
	// Producer-side InjectPrincipalFromContext OVERWRITES (mirror of
	// InjectObservabilityFromContext) — writer is the source of truth for
	// what the entry carries.
	e := &Entry{
		ID:        "evt-1",
		Principal: PrincipalMetadata{ActorID: "stale"},
	}
	ctx := ctxkeys.WithActor(context.Background(), "fresh")
	e.InjectPrincipalFromContext(ctx)
	assert.Equal(t, idutil.SafeID("fresh"), e.Principal.ActorID)
}
