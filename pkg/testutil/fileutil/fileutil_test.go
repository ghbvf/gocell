package fileutil_test

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/ghbvf/gocell/pkg/testutil/fileutil"
)

func TestMustReadFile_Success(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "data.txt")
	want := []byte("hello fileutil")
	fileutil.MustWriteFile(t, path, want)

	got := fileutil.MustReadFile(t, path)
	if !bytes.Equal(got, want) {
		t.Fatalf("MustReadFile got %q, want %q", got, want)
	}
}

func TestMustWriteFile_Success(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "out.bin")
	payload := []byte{0x01, 0x02, 0x03}

	fileutil.MustWriteFile(t, path, payload)

	got := fileutil.MustReadFile(t, path)
	if !bytes.Equal(got, payload) {
		t.Fatalf("MustWriteFile produced %x, want %x", got, payload)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("MustWriteFile mode = %o, want 0o600", info.Mode().Perm())
	}
}

func TestMustReadFile_FailFatal(t *testing.T) {
	t.Parallel()
	probe := &fakeT{TB: t}
	dir := t.TempDir()
	missing := filepath.Join(dir, "does-not-exist")

	func() {
		defer func() {
			if p := recover(); p != nil && p != fatalSentinel {
				panic(p) // surface unexpected panic instead of swallowing it
			}
		}()
		fileutil.MustReadFile(probe, missing)
	}()
	if !probe.failed {
		t.Fatalf("MustReadFile on missing path did not call Fatalf")
	}
}

func TestMustWriteFile_FailFatal(t *testing.T) {
	t.Parallel()
	probe := &fakeT{TB: t}
	bogus := filepath.Join(t.TempDir(), "no-such-dir", "out")

	func() {
		defer func() {
			if p := recover(); p != nil && p != fatalSentinel {
				panic(p) // surface unexpected panic instead of swallowing it
			}
		}()
		fileutil.MustWriteFile(probe, bogus, []byte("x"))
	}()
	if !probe.failed {
		t.Fatalf("MustWriteFile to invalid path did not call Fatalf")
	}
}

// fatalSentinel is the recover() value Fatalf panics with so callers can
// distinguish "Fatalf fired as expected" from "an unexpected panic happened".
const fatalSentinel = "fakeT.Fatalf"

// fakeT captures Fatalf so error-path tests can assert it fired without
// aborting the parent test. Only Fatalf and Helper are exercised; panic
// interrupts the helper before any other testing.TB method is reached.
type fakeT struct {
	testing.TB
	failed bool
}

func (f *fakeT) Fatalf(string, ...any) {
	f.failed = true
	panic(fatalSentinel)
}

func (f *fakeT) Helper() {}
