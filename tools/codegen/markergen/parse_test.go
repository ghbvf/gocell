package markergen

import (
	"fmt"
	"strings"
	"testing"
)

func TestSplitMarker(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		line   string
		wantN  string
		wantKV string
		wantOK bool
	}{
		{
			name:   "listener marker",
			line:   "// +cell:listener:ref=cell.PrimaryListener,prefix=/api/v1",
			wantN:  "cell:listener",
			wantKV: "ref=cell.PrimaryListener,prefix=/api/v1",
			wantOK: true,
		},
		{
			name:   "route marker",
			line:   "// +slice:route:slice=ordercreate,subPath=/orders",
			wantN:  "slice:route",
			wantKV: "slice=ordercreate,subPath=/orders",
			wantOK: true,
		},
		{
			name:   "subscribe marker",
			line:   "// +slice:subscribe:slice=configsubscribe,topic=event.config.entry-upserted.v1,handler=HandleEntryUpserted,group=configcore",
			wantN:  "slice:subscribe",
			wantKV: "slice=configsubscribe,topic=event.config.entry-upserted.v1,handler=HandleEntryUpserted,group=configcore",
			wantOK: true,
		},
		{
			name:   "not a marker comment",
			line:   "// normal comment",
			wantOK: false,
		},
		{
			name:   "empty line",
			line:   "",
			wantOK: false,
		},
		{
			name:   "other tool marker ignored",
			line:   "// +kubebuilder:object:root=true",
			wantOK: false,
		},
		{
			name:   "marker with leading space",
			line:   "  // +cell:listener:ref=cell.PrimaryListener",
			wantN:  "cell:listener",
			wantKV: "ref=cell.PrimaryListener",
			wantOK: true,
		},
		{
			name:   "marker name only no body",
			line:   "// +cell:listener",
			wantN:  "cell:listener",
			wantKV: "",
			wantOK: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			n, kv, ok := splitMarker(tc.line)
			if ok != tc.wantOK {
				t.Errorf("ok=%v, want %v", ok, tc.wantOK)
			}
			if !tc.wantOK {
				return
			}
			if n != tc.wantN {
				t.Errorf("name=%q, want %q", n, tc.wantN)
			}
			if kv != tc.wantKV {
				t.Errorf("kv=%q, want %q", kv, tc.wantKV)
			}
		})
	}
}

func TestParseKV(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		input   string
		want    map[string]string
		wantErr bool
	}{
		{
			name:  "simple two fields",
			input: "ref=cell.PrimaryListener,prefix=/api/v1",
			want:  map[string]string{"ref": "cell.PrimaryListener", "prefix": "/api/v1"},
		},
		{
			name:  "single field",
			input: "slice=ordercreate",
			want:  map[string]string{"slice": "ordercreate"},
		},
		{
			name:  "empty value",
			input: "key=",
			want:  map[string]string{"key": ""},
		},
		{
			name:  "empty input",
			input: "",
			want:  map[string]string{},
		},
		{
			name:    "missing equals",
			input:   "badfield",
			wantErr: true,
		},
		{
			name:  "spaces around equals",
			input: "key = value",
			want:  map[string]string{"key": "value"},
		},
		{
			name:  "value with dots and slashes",
			input: "topic=event.config.entry-upserted.v1,handler=HandleEntryUpserted,group=configcore",
			want: map[string]string{
				"topic":   "event.config.entry-upserted.v1",
				"handler": "HandleEntryUpserted",
				"group":   "configcore",
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := parseKV(tc.input)
			if (err != nil) != tc.wantErr {
				t.Errorf("err=%v, wantErr=%v", err, tc.wantErr)
			}
			if tc.wantErr {
				return
			}
			if len(got) != len(tc.want) {
				t.Errorf("len=%d, want %d; got=%v want=%v", len(got), len(tc.want), got, tc.want)
				return
			}
			for k, v := range tc.want {
				if got[k] != v {
					t.Errorf("key %q: got %q, want %q", k, got[k], v)
				}
			}
		})
	}
}

func TestLevenshtein(t *testing.T) {
	t.Parallel()
	cases := []struct {
		a, b string
		want int
	}{
		{"", "", 0},
		{"abc", "", 3},
		{"", "abc", 3},
		{"abc", "abc", 0},
		{"abc", "abd", 1},
		{"kitten", "sitting", 3},
		{"cell:listener", "cell:listner", 1},
		{"slice:route", "slice:rote", 1},
	}
	for _, tc := range cases {
		t.Run(tc.a+"/"+tc.b, func(t *testing.T) {
			t.Parallel()
			got := levenshtein(tc.a, tc.b)
			if got != tc.want {
				t.Errorf("levenshtein(%q,%q)=%d, want %d", tc.a, tc.b, got, tc.want)
			}
		})
	}
}

func TestSuggestMarkerName(t *testing.T) {
	t.Parallel()
	known := []string{"cell:listener", "slice:route", "slice:subscribe"}
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "exact match",
			input: "cell:listener",
			want:  "cell:listener",
		},
		{
			name:  "one typo",
			input: "cell:listner",
			want:  "cell:listener",
		},
		{
			name:  "two typos",
			input: "slice:rout",
			want:  "slice:route",
		},
		{
			name:  "too far",
			input: "completely:wrong:name",
			want:  "",
		},
		{
			name:  "close to subscribe",
			input: "slice:subscrbe",
			want:  "slice:subscribe",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := suggestMarkerName(tc.input, known)
			if got != tc.want {
				t.Errorf("suggest(%q)=%q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

// TestParseRoute_SubPathPresence tests that subPath must be explicitly declared.
// An absent subPath key is rejected; an explicit empty value (subPath=) is accepted.
func TestParseRoute_SubPathPresence(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name        string
		kvLine      string
		wantErr     bool
		wantErrSub  string
		wantSubPath string
	}{
		{
			name:        "subPath present with value",
			kvLine:      "slice=ordercreate,subPath=/orders",
			wantSubPath: "/orders",
		},
		{
			name:        "subPath explicit empty (mount on prefix root)",
			kvLine:      "slice=ordercreate,subPath=",
			wantSubPath: "",
		},
		{
			name:       "subPath absent — rejected",
			kvLine:     "slice=ordercreate",
			wantErr:    true,
			wantErrSub: `missing required field "subPath"`,
		},
		{
			name:       "typo subPth= — unknown field + missing subPath",
			kvLine:     "slice=ordercreate,subPth=/orders",
			wantErr:    true,
			wantErrSub: `missing required field "subPath"`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := collectedMarker{Name: "slice:route", KVLine: tc.kvLine, Line: 1}
			got, err := parseRoute(m)
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				if tc.wantErrSub != "" && !strings.Contains(err.Error(), tc.wantErrSub) {
					t.Errorf("error %q missing expected substring %q", err.Error(), tc.wantErrSub)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got.SubPath != tc.wantSubPath {
				t.Errorf("SubPath=%q, want %q", got.SubPath, tc.wantSubPath)
			}
		})
	}
}

func TestErrList(t *testing.T) {
	t.Parallel()
	t.Run("empty returns nil", func(t *testing.T) {
		t.Parallel()
		var e errList
		if e.AsError() != nil {
			t.Error("expected nil")
		}
	})
	t.Run("single error", func(t *testing.T) {
		t.Parallel()
		var e errList
		e.Append(fmt.Errorf("one"))
		err := e.AsError()
		if err == nil || err.Error() != "one" {
			t.Errorf("got %v", err)
		}
	})
	t.Run("multiple errors joined", func(t *testing.T) {
		t.Parallel()
		var e errList
		e.Append(fmt.Errorf("alpha"))
		e.Append(fmt.Errorf("beta"))
		err := e.AsError()
		if err == nil {
			t.Fatal("expected error")
		}
		s := err.Error()
		if !strings.Contains(s, "alpha") || !strings.Contains(s, "beta") {
			t.Errorf("missing messages: %q", s)
		}
	})
	t.Run("nil append ignored", func(t *testing.T) {
		t.Parallel()
		var e errList
		e.Append(nil)
		if e.AsError() != nil {
			t.Error("expected nil after appending nil")
		}
	})
}
