package domain

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestNewNonEmpty_RejectsEmpty(t *testing.T) {
	t.Parallel()
	_, err := NewNonEmpty("")
	if !errors.Is(err, ErrEmpty) {
		t.Fatalf("NewNonEmpty(\"\"): want ErrEmpty, got %v", err)
	}
}

func TestNewNonEmpty_AcceptsNonEmpty(t *testing.T) {
	t.Parallel()
	n, err := NewNonEmpty("alice")
	if err != nil {
		t.Fatalf("NewNonEmpty(\"alice\"): unexpected err %v", err)
	}
	if string(n) != "alice" {
		t.Errorf("NewNonEmpty value: got %q, want %q", string(n), "alice")
	}
}

func TestNonEmpty_UnmarshalJSON(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		input   string
		wantErr bool
		wantVal string
	}{
		{name: "non-empty string", input: `"alice"`, wantErr: false, wantVal: "alice"},
		{name: "empty string", input: `""`, wantErr: true},
		{name: "json number", input: `42`, wantErr: true},
		// JSON null targeting a non-pointer NonEmpty is rejected — NonEmpty
		// cannot represent the empty/absent state. For PATCH-style "absent"
		// semantics use *NonEmpty; json package short-circuits null → nil
		// pointer and never invokes UnmarshalJSON (covered by PATCHSemantics
		// test below).
		{name: "json null on non-pointer", input: `null`, wantErr: true},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var n NonEmpty
			err := json.Unmarshal([]byte(tc.input), &n)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("UnmarshalJSON(%q): want error, got nil", tc.input)
				}
				return
			}
			if err != nil {
				t.Fatalf("UnmarshalJSON(%q): unexpected err %v", tc.input, err)
			}
			if string(n) != tc.wantVal {
				t.Errorf("UnmarshalJSON(%q): got %q, want %q", tc.input, string(n), tc.wantVal)
			}
		})
	}
}

// TestNonEmpty_UnmarshalJSON_PATCHSemantics pins the wire-level PATCH contract:
// in a struct with `Field *NonEmpty`, JSON null → nil pointer (PATCH "not
// provided"), JSON empty string → decode error (caller bug, 400). Non-empty
// JSON string → non-nil pointer to validated value.
func TestNonEmpty_UnmarshalJSON_PATCHSemantics(t *testing.T) {
	t.Parallel()
	type patch struct {
		Name *NonEmpty `json:"name"`
	}

	t.Run("null field → nil pointer", func(t *testing.T) {
		t.Parallel()
		var p patch
		if err := json.Unmarshal([]byte(`{"name": null}`), &p); err != nil {
			t.Fatalf("unmarshal null: %v", err)
		}
		if p.Name != nil {
			t.Errorf("Name: got %q, want nil", *p.Name)
		}
	})

	t.Run("absent field → nil pointer", func(t *testing.T) {
		t.Parallel()
		var p patch
		if err := json.Unmarshal([]byte(`{}`), &p); err != nil {
			t.Fatalf("unmarshal absent: %v", err)
		}
		if p.Name != nil {
			t.Errorf("Name: got %q, want nil", *p.Name)
		}
	})

	t.Run("empty string field → decode error", func(t *testing.T) {
		t.Parallel()
		var p patch
		err := json.Unmarshal([]byte(`{"name": ""}`), &p)
		if !errors.Is(err, ErrEmpty) {
			t.Fatalf("empty string PATCH: want ErrEmpty, got %v", err)
		}
	})

	t.Run("non-empty string field → set value", func(t *testing.T) {
		t.Parallel()
		var p patch
		if err := json.Unmarshal([]byte(`{"name": "bob"}`), &p); err != nil {
			t.Fatalf("unmarshal non-empty: %v", err)
		}
		if p.Name == nil || string(*p.Name) != "bob" {
			t.Errorf("Name: got %v, want bob", p.Name)
		}
	})
}

func TestNonEmpty_MarshalJSON(t *testing.T) {
	t.Parallel()
	n := NonEmpty("alice")
	out, err := json.Marshal(n)
	if err != nil {
		t.Fatalf("MarshalJSON: %v", err)
	}
	if got := string(out); got != `"alice"` {
		t.Errorf("MarshalJSON: got %s, want \"alice\"", got)
	}
}
