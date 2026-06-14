package projection

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"

	"github.com/ghbvf/gocell/framework/pkg/authz"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/pkg/redaction"
)

// marshalExpected encodes want with the same encoder ResourceProjection.MarshalJSON
// uses (stdlib json.Marshal, which HTML-escapes "<"/">" in the redaction.Mask
// sentinel and sorts map keys). Comparing against this keeps the test honest
// about the on-wire bytes instead of hand-writing escaped literals.
func marshalExpected(t *testing.T, want map[string]any) []byte {
	t.Helper()
	b, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("marshal expected: %v", err)
	}
	return b
}

func TestNewProjection(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		mask    authz.FieldMask
		data    map[string]any
		want    map[string]any // expected visible view (post-mask); nil ⇒ expect error
		wantErr bool
	}{
		{
			name: "identity projection / empty mask keeps every value",
			mask: authz.FieldMask{},
			data: map[string]any{"id": "u1", "email": "a@b.c", "age": 30},
			want: map[string]any{"id": "u1", "email": "a@b.c", "age": 30},
		},
		{
			name: "single column masked, field set unchanged",
			mask: authz.FieldMask{Fields: []string{"email"}},
			data: map[string]any{"id": "u1", "email": "a@b.c", "age": 30},
			want: map[string]any{"id": "u1", "email": redaction.Mask, "age": 30},
		},
		{
			name: "multiple columns masked",
			mask: authz.FieldMask{Fields: []string{"email", "age"}},
			data: map[string]any{"id": "u1", "email": "a@b.c", "age": 30},
			want: map[string]any{"id": "u1", "email": redaction.Mask, "age": redaction.Mask},
		},
		{
			name: "masked column absent in row is a no-op (no key materialized)",
			mask: authz.FieldMask{Fields: []string{"ssn"}},
			data: map[string]any{"id": "u1", "email": "a@b.c"},
			want: map[string]any{"id": "u1", "email": "a@b.c"},
		},
		{
			name: "nested value under a masked key is replaced wholesale (no sub-field leak)",
			mask: authz.FieldMask{Fields: []string{"profile"}},
			data: map[string]any{"id": "u1", "profile": map[string]any{"ssn": "secret"}},
			want: map[string]any{"id": "u1", "profile": redaction.Mask},
		},
		{
			name:    "non-canonical mask key fails (authz.FieldMask.Validate)",
			mask:    authz.FieldMask{Fields: []string{" email"}},
			data:    map[string]any{"email": "a@b.c"},
			wantErr: true,
		},
		{
			name:    "duplicate mask column fails (authz.FieldMask.Validate)",
			mask:    authz.FieldMask{Fields: []string{"email", "email"}},
			data:    map[string]any{"email": "a@b.c"},
			wantErr: true,
		},
		{
			name:    "dotted mask field fails closed (PEP cannot discharge nested obligation)",
			mask:    authz.FieldMask{Fields: []string{"profile.ssn"}},
			data:    map[string]any{"profile": map[string]any{"ssn": "secret"}},
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := NewProjection(tc.mask, tc.data)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("NewProjection(%v) = nil error, want error", tc.mask)
				}
				return
			}
			if err != nil {
				t.Fatalf("NewProjection(%v) unexpected error: %v", tc.mask, err)
			}
			gotJSON, err := json.Marshal(got)
			if err != nil {
				t.Fatalf("MarshalJSON: %v", err)
			}
			if want := marshalExpected(t, tc.want); !bytes.Equal(gotJSON, want) {
				t.Errorf("projected view = %s, want %s", gotJSON, want)
			}
		})
	}
}

// TestNewProjection_DottedMaskFailClosedCode pins that an un-dischargeable
// (dotted) obligation fails closed with the dedicated KindInternal
// ErrAuthObligationUnenforceable code — so operators can alert on it distinctly
// from arbitrary 500s, and so the masker never serves an un-masked column.
func TestNewProjection_DottedMaskFailClosedCode(t *testing.T) {
	t.Parallel()

	_, err := NewProjection(authz.FieldMask{Fields: []string{"profile.ssn"}}, map[string]any{"profile": "x"})
	if err == nil {
		t.Fatal("NewProjection accepted a dotted mask, want fail-closed error")
	}
	var ce *errcode.Error
	if !errors.As(err, &ce) {
		t.Fatalf("error is not *errcode.Error: %v", err)
	}
	if ce.Code != errcode.ErrAuthObligationUnenforceable {
		t.Errorf("code = %s, want %s", ce.Code, errcode.ErrAuthObligationUnenforceable)
	}
	if ce.Kind != errcode.KindInternal {
		t.Errorf("kind = %v, want KindInternal (fail-closed 500)", ce.Kind)
	}
}

// TestNewProjection_DefensiveCopy proves the caller's input map (and the nested
// object under a masked key) is never mutated by masking — masking operates on a
// private copy, so a caller that retains the raw map still sees the raw values.
func TestNewProjection_DefensiveCopy(t *testing.T) {
	t.Parallel()

	nested := map[string]any{"ssn": "secret"}
	data := map[string]any{"id": "u1", "email": "a@b.c", "profile": nested}

	if _, err := NewProjection(authz.FieldMask{Fields: []string{"email", "profile"}}, data); err != nil {
		t.Fatalf("NewProjection: %v", err)
	}

	if data["email"] != "a@b.c" {
		t.Errorf("input map mutated: email = %v, want a@b.c", data["email"])
	}
	if data["profile"].(map[string]any)["ssn"] != "secret" {
		t.Errorf("input nested map mutated: profile.ssn = %v, want secret", nested["ssn"])
	}
}

func TestNewProjectionList(t *testing.T) {
	t.Parallel()

	t.Run("masks every row independently", func(t *testing.T) {
		t.Parallel()
		rows := []map[string]any{
			{"id": "u1", "email": "a@b.c"},
			{"id": "u2", "email": "d@e.f"},
		}
		got, err := NewProjectionList(authz.FieldMask{Fields: []string{"email"}}, rows)
		if err != nil {
			t.Fatalf("NewProjectionList: %v", err)
		}
		if len(got) != 2 {
			t.Fatalf("len = %d, want 2", len(got))
		}
		for i, p := range got {
			b, err := json.Marshal(p)
			if err != nil {
				t.Fatalf("row %d MarshalJSON: %v", i, err)
			}
			want := marshalExpected(t, map[string]any{"id": rows[i]["id"], "email": redaction.Mask})
			if !bytes.Equal(b, want) {
				t.Errorf("row %d = %s, want %s", i, b, want)
			}
		}
	})

	t.Run("empty rows yields empty slice", func(t *testing.T) {
		t.Parallel()
		got, err := NewProjectionList(authz.FieldMask{Fields: []string{"email"}}, nil)
		if err != nil {
			t.Fatalf("NewProjectionList: %v", err)
		}
		if len(got) != 0 {
			t.Errorf("len = %d, want 0", len(got))
		}
	})

	t.Run("invalid mask fails the whole call before any row", func(t *testing.T) {
		t.Parallel()
		rows := []map[string]any{{"email": "a@b.c"}}
		if _, err := NewProjectionList(authz.FieldMask{Fields: []string{"email", "email"}}, rows); err == nil {
			t.Error("NewProjectionList accepted a duplicate-column mask, want error")
		}
	})

	t.Run("dotted mask field fails closed", func(t *testing.T) {
		t.Parallel()
		rows := []map[string]any{{"profile": map[string]any{"ssn": "x"}}}
		if _, err := NewProjectionList(authz.FieldMask{Fields: []string{"profile.ssn"}}, rows); err == nil {
			t.Error("NewProjectionList accepted a dotted mask, want fail-closed error")
		}
	})
}

// TestResourceProjection_ZeroValueInert documents the seal's blind spot: a
// zero-value ResourceProjection{} (the only literal expressible outside this
// package) carries no forged data — it marshals to JSON null. A constructed
// projection over a nil data map, by contrast, marshals to an empty object "{}"
// — the two empty forms are deliberately distinguishable.
func TestResourceProjection_ZeroValueInert(t *testing.T) {
	t.Parallel()

	b, err := json.Marshal(ResourceProjection{})
	if err != nil {
		t.Fatalf("MarshalJSON zero value: %v", err)
	}
	if string(b) != "null" {
		t.Errorf("zero ResourceProjection marshaled to %s, want null", b)
	}

	p, err := NewProjection(authz.FieldMask{Fields: []string{"email"}}, nil)
	if err != nil {
		t.Fatalf("NewProjection(mask, nil): %v", err)
	}
	nb, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("MarshalJSON nil-data projection: %v", err)
	}
	if string(nb) != "{}" {
		t.Errorf("constructed projection over nil data marshaled to %s, want {}", nb)
	}
}

// TestNewProjectionList_NilRow proves a nil row element is projected to an empty
// object ("{}") rather than panicking or producing a null element.
func TestNewProjectionList_NilRow(t *testing.T) {
	t.Parallel()

	got, err := NewProjectionList(authz.FieldMask{Fields: []string{"email"}}, []map[string]any{nil})
	if err != nil {
		t.Fatalf("NewProjectionList with nil row: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1", len(got))
	}
	b, err := json.Marshal(got[0])
	if err != nil {
		t.Fatalf("MarshalJSON nil row: %v", err)
	}
	if string(b) != "{}" {
		t.Errorf("nil row marshaled to %s, want {}", b)
	}
}
