package wrapper_test

import (
	"testing"

	"github.com/ghbvf/gocell/kernel/wrapper"
)

func TestContractSpec_HTTPSpec_Validate(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		spec    wrapper.ContractSpec
		wantErr bool
	}{
		{"happy — full http spec", wrapper.ContractSpec{
			ID: "http.auth.login.v1", Kind: "http", Transport: "http",
			Method: "POST", Path: "/api/v1/auth/login"}, false},
		{"empty id rejected", wrapper.ContractSpec{Kind: "http", Transport: "http", Method: "POST", Path: "/x"}, true},
		{"empty kind rejected", wrapper.ContractSpec{ID: "a", Transport: "http", Method: "POST", Path: "/x"}, true},
		{"empty transport rejected", wrapper.ContractSpec{ID: "a", Kind: "http", Method: "POST", Path: "/x"}, true},
		{"http kind requires method", wrapper.ContractSpec{ID: "a", Kind: "http", Transport: "http", Path: "/x"}, true},
		{"http kind requires path", wrapper.ContractSpec{ID: "a", Kind: "http", Transport: "http", Method: "POST"}, true},
		{"path must start with slash", wrapper.ContractSpec{ID: "a", Kind: "http", Transport: "http", Method: "POST", Path: "nope"}, true},
		{"method must be upper case", wrapper.ContractSpec{ID: "a", Kind: "http", Transport: "http", Method: "post", Path: "/x"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.spec.Validate()
			if tc.wantErr && err == nil {
				t.Fatalf("expected error for %+v, got nil", tc.spec)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error %v for %+v", err, tc.spec)
			}
		})
	}
}

// TestEventSpec_Helper verifies the id==topic convenience constructor sets
// every field expected by Validate() and the consumer/handler pipelines.
func TestEventSpec_Helper(t *testing.T) {
	t.Parallel()
	spec := wrapper.EventSpec("event.session.revoked.v1", "amqp")
	if spec.ID != "event.session.revoked.v1" {
		t.Errorf("ID: %q", spec.ID)
	}
	if spec.Kind != "event" {
		t.Errorf("Kind: %q", spec.Kind)
	}
	if spec.Transport != "amqp" {
		t.Errorf("Transport: %q", spec.Transport)
	}
	if spec.Topic != "event.session.revoked.v1" {
		t.Errorf("Topic: %q", spec.Topic)
	}
	if err := spec.Validate(); err != nil {
		t.Fatalf("EventSpec should produce a valid spec, got %v", err)
	}
}

func TestContractSpec_EventSpec_Validate(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		spec    wrapper.ContractSpec
		wantErr bool
	}{
		{"happy — event spec", wrapper.ContractSpec{
			ID: "event.session.revoked.v1", Kind: "event", Transport: "amqp",
			Topic: "session.revoked.v1"}, false},
		{"event kind requires topic", wrapper.ContractSpec{ID: "a", Kind: "event", Transport: "amqp"}, true},
		{"event spec with http fields rejected", wrapper.ContractSpec{
			ID: "a", Kind: "event", Transport: "amqp", Topic: "t", Method: "POST"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.spec.Validate()
			if tc.wantErr && err == nil {
				t.Fatalf("expected error for %+v, got nil", tc.spec)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error %v for %+v", err, tc.spec)
			}
		})
	}
}
