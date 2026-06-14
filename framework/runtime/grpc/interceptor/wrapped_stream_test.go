package interceptor

import (
	"context"
	"testing"
)

type wsCtxKey struct{ n int }

func TestWrapServerStream_OverridesContext(t *testing.T) {
	base := &fakeServerStream{ctx: context.Background()}
	newCtx := context.WithValue(context.Background(), wsCtxKey{1}, "v")
	w := wrapServerStream(base, newCtx)
	if w.Context() != newCtx {
		t.Fatalf("wrapServerStream Context() must return the supplied ctx")
	}
}

func TestWrapServerStream_RewrapInPlace(t *testing.T) {
	base := &fakeServerStream{ctx: context.Background()}
	c1 := context.WithValue(context.Background(), wsCtxKey{1}, "a")
	w1 := wrapServerStream(base, c1)

	c2 := context.WithValue(c1, wsCtxKey{2}, "b")
	w2 := wrapServerStream(w1, c2)

	if w2 != w1 {
		t.Fatalf("re-wrapping an already-wrapped stream must reuse the existing wrapper, not nest")
	}
	if w2.Context() != c2 {
		t.Fatalf("re-wrap must replace the context in place")
	}
}

func TestWrapServerStream_PassesThroughEmbeddedMethods(t *testing.T) {
	base := &fakeServerStream{ctx: context.Background()}
	w := wrapServerStream(base, context.Background())
	if err := w.SendMsg("x"); err != nil {
		t.Fatalf("SendMsg passthrough: %v", err)
	}
	if len(base.sent) != 1 || base.sent[0] != "x" {
		t.Fatalf("SendMsg must pass through to the embedded ServerStream, got %v", base.sent)
	}
}
