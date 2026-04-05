package auditarchive

import (
	"context"
	"errors"
	"testing"

	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestService_Archive_ReturnsNotImplemented(t *testing.T) {
	svc := NewService()
	err := svc.Archive(context.Background())
	require.Error(t, err)

	var ecErr *errcode.Error
	require.True(t, errors.As(err, &ecErr))
	assert.Equal(t, errcode.Code("ERR_NOT_IMPLEMENTED"), ecErr.Code)
}
