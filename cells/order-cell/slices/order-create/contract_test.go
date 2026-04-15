package ordercreate

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/cells/order-cell/internal/mem"
	"github.com/ghbvf/gocell/pkg/contracttest"
)

func newContractHandler() (http.Handler, *recordingWriter) {
	repo := mem.NewOrderRepository()
	writer := &recordingWriter{}
	svc := NewService(repo, slog.Default(), WithOutboxWriter(writer), WithTxManager(&stubTxRunner{}))
	mux := http.NewServeMux()
	mux.Handle("POST /api/v1/orders/", http.HandlerFunc(NewHandler(svc).HandleCreate))
	return mux, writer
}

func TestHttpOrderCreateV1Serve(t *testing.T) {
	root := contracttest.ContractsRoot()
	c := contracttest.LoadByID(t, root, "http.order.create.v1")
	h, _ := newContractHandler()

	c.ValidateRequest(t, []byte(`{"item":"widget"}`))
	c.MustRejectRequest(t, []byte(`{"item":"x","extra":"bad"}`))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(c.HTTP.Method, c.HTTP.Path, strings.NewReader(`{"item":"widget"}`))
	req.Header.Set("Content-Type", "application/json")
	h.ServeHTTP(rec, req)
	c.ValidateHTTPResponseRecorder(t, rec)
}

func TestEventOrderCreatedV1Publish(t *testing.T) {
	root := contracttest.ContractsRoot()
	httpContract := contracttest.LoadByID(t, root, "http.order.create.v1")
	c := contracttest.LoadByID(t, root, "event.order-created.v1")
	h, writer := newContractHandler()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(httpContract.HTTP.Method, httpContract.HTTP.Path, strings.NewReader(`{"item":"widget"}`))
	req.Header.Set("Content-Type", "application/json")
	h.ServeHTTP(rec, req)
	httpContract.ValidateHTTPResponseRecorder(t, rec)

	if len(writer.entries) != 1 {
		t.Fatalf("expected one emitted outbox entry, got %d", len(writer.entries))
	}
	entry := writer.entries[0]
	c.ValidatePayload(t, entry.Payload)
	c.ValidateHeaders(t, []byte(`{"event_id":"`+entry.ID+`"}`))
	c.MustRejectPayload(t, []byte(`{"id":"o-1"}`))
	c.MustRejectHeaders(t, []byte(`{}`))

	var payload struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(entry.Payload, &payload); err != nil {
		t.Fatalf("decode emitted payload: %v", err)
	}
	if payload.ID == "" {
		t.Fatal("emitted payload did not include order id")
	}
}
