package query

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestPageRequest_Normalize_Default(t *testing.T) {
	var pr PageRequest
	pr.Normalize()
	assert.Equal(t, DefaultPageSize, pr.Limit)
}

func TestPageRequest_Normalize_ClampsMax(t *testing.T) {
	pr := PageRequest{Limit: 1000}
	pr.Normalize()
	assert.Equal(t, MaxPageSize, pr.Limit)
}

func TestPageRequest_Normalize_ClampsMin(t *testing.T) {
	tests := []struct {
		name  string
		limit int
	}{
		{"zero", 0},
		{"negative", -1},
		{"negative large", -100},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pr := PageRequest{Limit: tt.limit}
			pr.Normalize()
			assert.Equal(t, DefaultPageSize, pr.Limit)
		})
	}
}

func TestPageRequest_Normalize_KeepsValid(t *testing.T) {
	pr := PageRequest{Limit: 100}
	pr.Normalize()
	assert.Equal(t, 100, pr.Limit)
}

func TestPageRequest_Normalize_PreservesCursor(t *testing.T) {
	pr := PageRequest{Limit: 0, Cursor: "some-token"}
	pr.Normalize()
	assert.Equal(t, DefaultPageSize, pr.Limit)
	assert.Equal(t, "some-token", pr.Cursor)
}

func TestListParams_FetchLimit(t *testing.T) {
	lp := ListParams{Limit: 50}
	assert.Equal(t, 51, lp.FetchLimit())
}

func TestListParams_FetchLimit_One(t *testing.T) {
	lp := ListParams{Limit: 1}
	assert.Equal(t, 2, lp.FetchLimit())
}
