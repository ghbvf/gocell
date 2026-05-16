package errcodetest_test

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/errcode/errcodetest"
)

// fakeTB captures testing.TB Fatalf/Errorf calls without halting the parent
// test. It satisfies the subset of testing.TB the funnels call (Helper /
// Errorf / Fatalf). Fatalf records the failure and panics with a sentinel so
// the test can detect "fatal path" via recover.
type fakeTB struct {
	testing.TB
	errorMsgs []string
	fatalMsgs []string
}

type fatalSentinel struct{}

func (f *fakeTB) Helper() {}

func (f *fakeTB) Errorf(format string, args ...any) {
	f.errorMsgs = append(f.errorMsgs, fmt.Sprintf(format, args...))
}

func (f *fakeTB) Fatalf(format string, args ...any) {
	f.fatalMsgs = append(f.fatalMsgs, fmt.Sprintf(format, args...))
	panic(fatalSentinel{})
}

// runCapturing invokes fn, capturing the fatalSentinel panic if AssertCode /
// AssertWireCode took the Fatalf path, so the caller can inspect both error
// and fatal slices.
func runCapturing(fn func()) (recovered bool) {
	defer func() {
		if r := recover(); r != nil {
			if _, ok := r.(fatalSentinel); ok {
				recovered = true
				return
			}
			panic(r)
		}
	}()
	fn()
	return false
}

func TestAssertCode_Compliant(t *testing.T) {
	ec := errcode.New(errcode.KindNotFound, errcode.ErrSessionNotFound, "session not found")
	tb := &fakeTB{}
	errcodetest.AssertCode(tb, ec, errcode.ErrSessionNotFound)
	if len(tb.errorMsgs) != 0 || len(tb.fatalMsgs) != 0 {
		t.Fatalf("expected no failure; errors=%v fatals=%v", tb.errorMsgs, tb.fatalMsgs)
	}
}

func TestAssertCode_CompliantWrapped(t *testing.T) {
	ec := errcode.New(errcode.KindNotFound, errcode.ErrConfigRepoNotFound, "config not found")
	wrapped := fmt.Errorf("repo layer: %w", ec)
	tb := &fakeTB{}
	errcodetest.AssertCode(tb, wrapped, errcode.ErrConfigRepoNotFound)
	if len(tb.errorMsgs) != 0 || len(tb.fatalMsgs) != 0 {
		t.Fatalf("expected no failure on wrapped error; errors=%v fatals=%v", tb.errorMsgs, tb.fatalMsgs)
	}
}

func TestAssertCode_NilErrFatal(t *testing.T) {
	tb := &fakeTB{}
	fatal := runCapturing(func() {
		errcodetest.AssertCode(tb, nil, errcode.ErrSessionNotFound)
	})
	if !fatal {
		t.Fatalf("expected Fatalf on nil err")
	}
	if got := strings.Join(tb.fatalMsgs, "|"); !strings.Contains(got, "got nil") {
		t.Errorf("fatal message must explain nil err: %q", got)
	}
}

func TestAssertCode_NonErrcodeChainFatal(t *testing.T) {
	plain := errors.New("plain sentinel")
	tb := &fakeTB{}
	fatal := runCapturing(func() {
		errcodetest.AssertCode(tb, plain, errcode.ErrSessionNotFound)
	})
	if !fatal {
		t.Fatalf("expected Fatalf on non-errcode error chain")
	}
	if got := strings.Join(tb.fatalMsgs, "|"); !strings.Contains(got, "*errcode.Error chain") {
		t.Errorf("fatal message must explain missing errcode chain: %q", got)
	}
}

func TestAssertCode_CodeMismatchNonFatal(t *testing.T) {
	ec := errcode.New(errcode.KindNotFound, errcode.ErrFlagNotFound, "flag not found")
	tb := &fakeTB{}
	fatal := runCapturing(func() {
		errcodetest.AssertCode(tb, ec, errcode.ErrSessionNotFound)
	})
	if fatal {
		t.Fatalf("Code mismatch must be Errorf (non-fatal), not Fatalf")
	}
	if len(tb.errorMsgs) != 1 {
		t.Fatalf("expected exactly one Errorf, got %d: %v", len(tb.errorMsgs), tb.errorMsgs)
	}
	if !strings.Contains(tb.errorMsgs[0], "Code mismatch") {
		t.Errorf("error message must mention Code mismatch: %q", tb.errorMsgs[0])
	}
}

// envelopeBody returns a fresh httptest.ResponseRecorder with the canonical
// wire envelope body for the given Error. Mirrors the production path —
// pkg/httputil.WriteError — without depending on it (pkg/errcode can't
// import pkg/httputil; pkg/httputil depends on pkg/errcode).
func envelopeBody(status int, ec *errcode.Error) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	rec.Code = status
	innerJSON, err := ec.MarshalJSON()
	if err != nil {
		panic(err)
	}
	rec.Body.WriteString(`{"error":`)
	rec.Body.Write(innerJSON)
	rec.Body.WriteString(`}`)
	return rec
}

func TestAssertWireCode_Compliant(t *testing.T) {
	rec := envelopeBody(http.StatusNotFound,
		errcode.New(errcode.KindNotFound, errcode.ErrConfigRepoNotFound, "config not found"))
	tb := &fakeTB{}
	errcodetest.AssertWireCode(tb, rec, http.StatusNotFound, errcode.ErrConfigRepoNotFound)
	if len(tb.errorMsgs) != 0 || len(tb.fatalMsgs) != 0 {
		t.Fatalf("expected no failure; errors=%v fatals=%v", tb.errorMsgs, tb.fatalMsgs)
	}
}

func TestAssertWireCode_NilRecFatal(t *testing.T) {
	tb := &fakeTB{}
	fatal := runCapturing(func() {
		errcodetest.AssertWireCode(tb, nil, http.StatusNotFound, errcode.ErrConfigRepoNotFound)
	})
	if !fatal {
		t.Fatalf("expected Fatalf on nil rec")
	}
}

func TestAssertWireCode_StatusMismatchFatal(t *testing.T) {
	rec := envelopeBody(http.StatusInternalServerError,
		errcode.New(errcode.KindInternal, errcode.ErrInternal, "boom"))
	tb := &fakeTB{}
	fatal := runCapturing(func() {
		errcodetest.AssertWireCode(tb, rec, http.StatusNotFound, errcode.ErrConfigRepoNotFound)
	})
	if !fatal {
		t.Fatalf("expected Fatalf on status mismatch")
	}
	if got := strings.Join(tb.fatalMsgs, "|"); !strings.Contains(got, "HTTP status mismatch") {
		t.Errorf("fatal message must mention status mismatch: %q", got)
	}
}

func TestAssertWireCode_EmptyBodyFatal(t *testing.T) {
	rec := httptest.NewRecorder()
	rec.Code = http.StatusNotFound
	tb := &fakeTB{}
	fatal := runCapturing(func() {
		errcodetest.AssertWireCode(tb, rec, http.StatusNotFound, errcode.ErrConfigRepoNotFound)
	})
	if !fatal {
		t.Fatalf("expected Fatalf on empty body")
	}
	if got := strings.Join(tb.fatalMsgs, "|"); !strings.Contains(got, "body is empty") {
		t.Errorf("fatal message must mention empty body: %q", got)
	}
}

func TestAssertWireCode_MalformedBodyFatal(t *testing.T) {
	rec := httptest.NewRecorder()
	rec.Code = http.StatusNotFound
	rec.Body.WriteString(`{not json`)
	tb := &fakeTB{}
	fatal := runCapturing(func() {
		errcodetest.AssertWireCode(tb, rec, http.StatusNotFound, errcode.ErrConfigRepoNotFound)
	})
	if !fatal {
		t.Fatalf("expected Fatalf on malformed body")
	}
	if got := strings.Join(tb.fatalMsgs, "|"); !strings.Contains(got, "wire envelope shape") {
		t.Errorf("fatal message must mention envelope shape: %q", got)
	}
}

func TestAssertWireCode_CodeMismatchNonFatal(t *testing.T) {
	rec := envelopeBody(http.StatusNotFound,
		errcode.New(errcode.KindNotFound, errcode.ErrFlagNotFound, "flag not found"))
	tb := &fakeTB{}
	fatal := runCapturing(func() {
		errcodetest.AssertWireCode(tb, rec, http.StatusNotFound, errcode.ErrConfigRepoNotFound)
	})
	if fatal {
		t.Fatalf("Code mismatch must be Errorf (non-fatal), not Fatalf")
	}
	if len(tb.errorMsgs) != 1 {
		t.Fatalf("expected exactly one Errorf, got %d: %v", len(tb.errorMsgs), tb.errorMsgs)
	}
	if !strings.Contains(tb.errorMsgs[0], "error.code mismatch") {
		t.Errorf("error message must mention error.code mismatch: %q", tb.errorMsgs[0])
	}
}
