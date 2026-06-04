package composition

import (
	"context"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/pkg/migration"
)

func stubMigrationFS() fstest.MapFS {
	return fstest.MapFS{
		"001_init.sql": &fstest.MapFile{Data: []byte("-- +goose Up\n-- +goose Down\n")},
	}
}

func TestBuilder_WithMigrations_AccumulatesInOrder(t *testing.T) {
	payment, err := migration.ParseNamespace("payment")
	require.NoError(t, err)
	billing, err := migration.ParseNamespace("billing")
	require.NoError(t, err)

	b := New("configcore").
		WithMigrations(payment, stubMigrationFS()).
		WithMigrations(billing, stubMigrationFS())

	regs := b.Migrations()
	require.Len(t, regs, 2)
	assert.Equal(t, payment, regs[0].Namespace)
	assert.Equal(t, billing, regs[1].Namespace, "registrations accumulate in call order")

	// Migrations() returns a copy: mutating it must not affect the builder.
	regs[0].Namespace = "tampered"
	assert.Equal(t, payment, b.Migrations()[0].Namespace, "Migrations() must return a defensive copy")
}

func TestBuilder_WithMigrations_NilWhenNoneRegistered(t *testing.T) {
	assert.Nil(t, New("configcore").Migrations())
}

func TestBuilder_Build_AcceptsValidMigration(t *testing.T) {
	ctx := context.Background()
	payment, err := migration.ParseNamespace("payment")
	require.NoError(t, err)

	mA := &fakeCellModule{id: "configcore", cell: stubCell("configcore")}
	app, err := New("configcore").
		With(mA).
		WithMigrations(payment, stubMigrationFS()).
		Build(ctx, minimalSharedDeps(t), noopRuntimeOpts)
	require.NoError(t, err)
	require.NotNil(t, app)
}

func TestBuilder_Build_RejectsBadMigration(t *testing.T) {
	tests := []struct {
		name    string
		apply   func(b *Builder) *Builder
		wantSub string
	}{
		{
			name: "invalid namespace conversion-literal",
			apply: func(b *Builder) *Builder {
				return b.WithMigrations(migration.Namespace("acme-payment"), stubMigrationFS())
			},
			wantSub: "invalid migration namespace",
		},
		{
			name: "reserved platform namespace rejected at Build",
			apply: func(b *Builder) *Builder {
				return b.WithMigrations(migration.PlatformNamespace, stubMigrationFS())
			},
			wantSub: "reserved for platform",
		},
		{
			name: "nil fs",
			apply: func(b *Builder) *Builder {
				ns, _ := migration.ParseNamespace("payment")
				return b.WithMigrations(ns, nil)
			},
			wantSub: "non-nil fs.FS",
		},
		{
			name: "duplicate namespace",
			apply: func(b *Builder) *Builder {
				ns, _ := migration.ParseNamespace("payment")
				return b.WithMigrations(ns, stubMigrationFS()).WithMigrations(ns, stubMigrationFS())
			},
			wantSub: "duplicate migration namespace",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			mA := &fakeCellModule{id: "configcore", cell: stubCell("configcore")}
			b := tt.apply(New("configcore").With(mA))
			_, err := b.Build(ctx, minimalSharedDeps(t), noopRuntimeOpts)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantSub)
			assert.False(t, mA.called, "bad migration registration must be rejected before Provide")
		})
	}
}
