package outboxtest

import (
	"context"
	"strings"
	"testing"
)

func TestTestTopic_UniquePerTest(t *testing.T) {
	topic1 := TestTopic(t)
	topic2 := TestTopic(t)

	if topic1 == topic2 {
		t.Fatal("TestTopic should return unique values per call")
	}
	if !strings.HasPrefix(topic1, "test-") {
		t.Fatalf("TestTopic should start with 'test-', got %q", topic1)
	}
	if !strings.Contains(topic1, t.Name()) {
		t.Fatalf("TestTopic should contain test name, got %q", topic1)
	}
}

func TestNewEntry_ValidFields(t *testing.T) {
	entry := NewEntry("my.topic", []byte(`{"k":"v"}`))

	if entry.ID == "" {
		t.Fatal("NewEntry ID must not be empty")
	}
	if !strings.HasPrefix(entry.ID, "evt-") {
		t.Fatalf("NewEntry ID should start with 'evt-', got %q", entry.ID)
	}
	if entry.Topic != "my.topic" {
		t.Fatalf("want topic 'my.topic', got %q", entry.Topic)
	}
	if entry.EventType != "my.topic" {
		t.Fatalf("want EventType 'my.topic', got %q", entry.EventType)
	}
	if string(entry.Payload) != `{"k":"v"}` {
		t.Fatalf("payload mismatch: %q", entry.Payload)
	}
	if entry.CreatedAt.IsZero() {
		t.Fatal("CreatedAt must not be zero")
	}
}

func TestNewEntry_UniqueIDs(t *testing.T) {
	e1 := NewEntry("t", []byte(`{}`))
	e2 := NewEntry("t", []byte(`{}`))
	if e1.ID == e2.ID {
		t.Fatal("NewEntry should generate unique IDs")
	}
}

func TestNewEntryWithMetadata(t *testing.T) {
	md := map[string]string{"trace_id": "abc"}
	entry := NewEntryWithMetadata("t", []byte(`{}`), md)

	if entry.Metadata == nil {
		t.Fatal("Metadata must not be nil")
	}
	if entry.Metadata["trace_id"] != "abc" {
		t.Fatalf("want trace_id 'abc', got %q", entry.Metadata["trace_id"])
	}
}

func TestMockReceipt_Commit(t *testing.T) {
	r := NewMockReceipt()

	if r.Committed() {
		t.Fatal("should not be committed initially")
	}
	if r.Released() {
		t.Fatal("should not be released initially")
	}

	if err := r.Commit(context.Background()); err != nil {
		t.Fatalf("Commit error: %v", err)
	}

	if !r.Committed() {
		t.Fatal("should be committed after Commit()")
	}
	if r.Released() {
		t.Fatal("should not be released after Commit()")
	}
}

func TestMockReceipt_Release(t *testing.T) {
	r := NewMockReceipt()

	if err := r.Release(context.Background()); err != nil {
		t.Fatalf("Release error: %v", err)
	}

	if !r.Released() {
		t.Fatal("should be released after Release()")
	}
	if r.Committed() {
		t.Fatal("should not be committed after Release()")
	}
}

func TestMockReceipt_WithErrors(t *testing.T) {
	commitErr := context.DeadlineExceeded
	releaseErr := context.Canceled

	r := NewMockReceiptWithErrors(commitErr, releaseErr)

	if err := r.Commit(context.Background()); err != commitErr {
		t.Fatalf("want commitErr, got %v", err)
	}
	if err := r.Release(context.Background()); err != releaseErr {
		t.Fatalf("want releaseErr, got %v", err)
	}

	// Even with errors, the state should be recorded.
	if !r.Committed() {
		t.Fatal("should be committed even when Commit returns error")
	}
	if !r.Released() {
		t.Fatal("should be released even when Release returns error")
	}
}

func TestFeatures_SetDefaults(t *testing.T) {
	f := Features{}
	f.setDefaults()

	if f.MessageCount == 0 {
		t.Fatal("MessageCount should have a non-zero default")
	}
	if f.MessageCount != 100 {
		t.Fatalf("want default MessageCount 100, got %d", f.MessageCount)
	}
}

func TestFeatures_SetDefaults_PreservesExplicit(t *testing.T) {
	f := Features{MessageCount: 42}
	f.setDefaults()

	if f.MessageCount != 42 {
		t.Fatalf("explicit MessageCount should be preserved, got %d", f.MessageCount)
	}
}
