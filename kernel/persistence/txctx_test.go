package persistence

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
)

// fakeTx stands in for a driver tx type (e.g. pgx.Tx) so this kernel test
// stays driver-agnostic — the package must not import any DB SDK.
type fakeTx interface{ id() string }

type fakeTxImpl struct{ name string }

func (f fakeTxImpl) id() string { return f.name }

// otherCarrier is an unrelated value type stored under the same key to prove
// TxFromContext fails the type assertion (returns zero, false) rather than
// panicking or coercing.
type otherCarrier struct{}

func TestTxFromContext(t *testing.T) {
	want := fakeTxImpl{name: "tx-1"}

	tests := []struct {
		name   string
		ctx    context.Context
		wantOK bool
		wantID string
	}{
		{
			name:   "no value present",
			ctx:    context.Background(),
			wantOK: false,
		},
		{
			name:   "value of T present",
			ctx:    context.WithValue(context.Background(), TxCtxKey, fakeTx(want)),
			wantOK: true,
			wantID: "tx-1",
		},
		{
			name:   "value of different type present",
			ctx:    context.WithValue(context.Background(), TxCtxKey, otherCarrier{}),
			wantOK: false,
		},
		{
			name:   "typed-nil interface stored",
			ctx:    context.WithValue(context.Background(), TxCtxKey, fakeTx(nil)),
			wantOK: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := TxFromContext[fakeTx](tt.ctx)
			assert.Equal(t, tt.wantOK, ok)
			if tt.wantOK {
				assert.Equal(t, tt.wantID, got.id())
			} else {
				assert.Nil(t, got)
			}
		})
	}
}
