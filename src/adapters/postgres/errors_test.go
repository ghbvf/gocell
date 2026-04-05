package postgres

import (
	"testing"

	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/stretchr/testify/assert"
)

func TestErrorCodes_Prefix(t *testing.T) {
	codes := []errcode.Code{
		ErrAdapterPGConnect,
		ErrAdapterPGQuery,
		ErrAdapterPGTxTimeout,
		ErrAdapterPGMigrate,
	}

	for _, c := range codes {
		assert.Contains(t, string(c), "ERR_ADAPTER_PG_",
			"error code %s must use ERR_ADAPTER_PG_ prefix", c)
	}
}

func TestErrorCodes_Unique(t *testing.T) {
	codes := []errcode.Code{
		ErrAdapterPGConnect,
		ErrAdapterPGQuery,
		ErrAdapterPGTxTimeout,
		ErrAdapterPGMigrate,
	}

	seen := make(map[errcode.Code]bool, len(codes))
	for _, c := range codes {
		assert.False(t, seen[c], "duplicate error code: %s", c)
		seen[c] = true
	}
}

func TestErrorCodes_CanCreateErrors(t *testing.T) {
	err := errcode.New(ErrAdapterPGConnect, "connection failed")
	assert.Equal(t, ErrAdapterPGConnect, err.Code)
	assert.Contains(t, err.Error(), "ERR_ADAPTER_PG_CONNECT")
}
