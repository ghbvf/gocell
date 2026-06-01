package interceptor

import (
	"context"

	kauth "github.com/ghbvf/gocell/kernel/auth"
	"github.com/ghbvf/gocell/kernel/wrapper"
)

// recordingTracer / recordingSpan capture span lifecycle for assertions.
type recordingTracer struct {
	span *recordingSpan
}

func (t *recordingTracer) Start(ctx context.Context, name string, attrs ...wrapper.Attr) (context.Context, wrapper.Span) {
	t.span.name = name
	t.span.SetAttributes(attrs...)
	return ctx, t.span
}

type recordingSpan struct {
	name        string
	attrs       []wrapper.Attr
	recordedErr error
	statusCode  wrapper.StatusCode
	statusDesc  string
	ended       bool
}

func (s *recordingSpan) SetAttributes(a ...wrapper.Attr)          { s.attrs = append(s.attrs, a...) }
func (s *recordingSpan) RecordError(err error)                    { s.recordedErr = err }
func (s *recordingSpan) SetStatus(c wrapper.StatusCode, d string) { s.statusCode = c; s.statusDesc = d }
func (s *recordingSpan) End()                                     { s.ended = true }

func (s *recordingSpan) attr(key string) (any, bool) {
	for _, a := range s.attrs {
		if a.Key == key {
			return a.Value, true
		}
	}
	return nil, false
}

// stubVerifier is a configurable IntentTokenVerifier for auth tests.
type stubVerifier struct {
	claims kauth.Claims
	err    error
}

func (v stubVerifier) VerifyIntent(_ context.Context, _ string, _ kauth.TokenIntent) (kauth.Claims, error) {
	return v.claims, v.err
}
