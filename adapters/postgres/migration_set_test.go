package postgres

import (
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/migration"
)

func fakeMigrationsFS() fstest.MapFS {
	return fstest.MapFS{
		"001_init.sql": &fstest.MapFile{Data: []byte("-- +goose Up\n-- +goose Down\n")},
	}
}

func TestTrackingTableFor(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "schema_migrations_platform", trackingTableFor(migration.PlatformNamespace),
		"platform is not special — it is namespace \"platform\" → schema_migrations_platform")
	assert.Equal(t, "schema_migrations_platform", PlatformTrackingTable)

	ns, err := migration.ParseNamespace("acme_payment")
	require.NoError(t, err)
	assert.Equal(t, "schema_migrations_acme_payment", trackingTableFor(ns))
}

func TestMigrationSet_Add(t *testing.T) {
	t.Parallel()

	t.Run("valid external namespace", func(t *testing.T) {
		t.Parallel()
		s := NewMigrationSet()
		ns, err := migration.ParseNamespace("payment")
		require.NoError(t, err)
		require.NoError(t, s.Add(ns, fakeMigrationsFS()))
		assert.Equal(t, []migration.Namespace{ns}, s.Namespaces())
	})

	t.Run("reserved platform namespace rejected", func(t *testing.T) {
		t.Parallel()
		s := NewMigrationSet()
		err := s.Add(migration.PlatformNamespace, fakeMigrationsFS())
		require.Error(t, err)
		var ec *errcode.Error
		require.ErrorAs(t, err, &ec)
		assert.Equal(t, ErrAdapterPGMigrate, ec.Code)
	})

	t.Run("duplicate namespace rejected", func(t *testing.T) {
		t.Parallel()
		s := NewMigrationSet()
		ns, err := migration.ParseNamespace("payment")
		require.NoError(t, err)
		require.NoError(t, s.Add(ns, fakeMigrationsFS()))
		require.Error(t, s.Add(ns, fakeMigrationsFS()), "duplicate namespace must be rejected")
	})

	t.Run("nil fs rejected", func(t *testing.T) {
		t.Parallel()
		s := NewMigrationSet()
		ns, err := migration.ParseNamespace("payment")
		require.NoError(t, err)
		require.Error(t, s.Add(ns, nil))
	})

	t.Run("invalid namespace conversion-literal rejected", func(t *testing.T) {
		t.Parallel()
		s := NewMigrationSet()
		require.Error(t, s.Add(migration.Namespace("acme-payment"), fakeMigrationsFS()))
	})
}

func TestNewMigrationSetWithPlatform_SeedsPlatformFirst(t *testing.T) {
	t.Parallel()
	s, err := NewMigrationSetWithPlatform()
	require.NoError(t, err)

	nss := s.Namespaces()
	require.NotEmpty(t, nss)
	assert.Equal(t, migration.PlatformNamespace, nss[0],
		"platform must be seeded first so external namespaces apply after it")

	ext, err := migration.ParseNamespace("payment")
	require.NoError(t, err)
	require.NoError(t, s.Add(ext, fakeMigrationsFS()))
	assert.Equal(t, []migration.Namespace{migration.PlatformNamespace, ext}, s.Namespaces(),
		"external namespaces append in registration order after platform")
}
