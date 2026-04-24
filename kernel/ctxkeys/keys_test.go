package ctxkeys

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCellIDRoundTrip(t *testing.T) {
	tests := []struct {
		name  string
		value string
	}{
		{name: "normal id", value: "accesscore"},
		{name: "empty string", value: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := WithCellID(context.Background(), tt.value)
			got, ok := CellIDFrom(ctx)
			assert.True(t, ok)
			assert.Equal(t, tt.value, got)
		})
	}
}

func TestSliceIDRoundTrip(t *testing.T) {
	tests := []struct {
		name  string
		value string
	}{
		{name: "normal id", value: "authlogin"},
		{name: "empty string", value: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := WithSliceID(context.Background(), tt.value)
			got, ok := SliceIDFrom(ctx)
			assert.True(t, ok)
			assert.Equal(t, tt.value, got)
		})
	}
}

func TestJourneyIDRoundTrip(t *testing.T) {
	tests := []struct {
		name  string
		value string
	}{
		{name: "journey id", value: "J-SSO-001"},
		{name: "empty string", value: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := WithJourneyID(context.Background(), tt.value)
			got, ok := JourneyIDFrom(ctx)
			assert.True(t, ok)
			assert.Equal(t, tt.value, got)
		})
	}
}

func TestContractIDRoundTrip(t *testing.T) {
	tests := []struct {
		name  string
		value string
	}{
		{name: "http contract id", value: "http.auth.login.v1"},
		{name: "event contract id", value: "event.session.revoked.v1"},
		{name: "empty string", value: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := WithContractID(context.Background(), tt.value)
			got, ok := ContractIDFrom(ctx)
			assert.True(t, ok)
			assert.Equal(t, tt.value, got)
		})
	}
}

func TestFromMissingKey(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name string
		fn   func(context.Context) (string, bool)
	}{
		{name: "CellID missing", fn: CellIDFrom},
		{name: "SliceID missing", fn: SliceIDFrom},
		{name: "JourneyID missing", fn: JourneyIDFrom},
		{name: "ContractID missing", fn: ContractIDFrom},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := tt.fn(ctx)
			assert.False(t, ok)
			assert.Equal(t, "", got)
		})
	}
}

func TestMultipleKeysInSameContext(t *testing.T) {
	ctx := context.Background()
	ctx = WithCellID(ctx, "accesscore")
	ctx = WithSliceID(ctx, "authlogin")
	ctx = WithJourneyID(ctx, "J-SSO-001")

	cellID, ok := CellIDFrom(ctx)
	assert.True(t, ok)
	assert.Equal(t, "accesscore", cellID)

	sliceID, ok := SliceIDFrom(ctx)
	assert.True(t, ok)
	assert.Equal(t, "authlogin", sliceID)

	journeyID, ok := JourneyIDFrom(ctx)
	assert.True(t, ok)
	assert.Equal(t, "J-SSO-001", journeyID)
}
