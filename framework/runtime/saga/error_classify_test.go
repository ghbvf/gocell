package saga

import (
	"errors"
	"fmt"
	"log/slog"
	"testing"

	"github.com/ghbvf/gocell/framework/pkg/errcode"
)

func TestJournalErrLevel(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		err  error
		want slog.Level
	}{
		{
			name: "nil err defaults to Warn",
			err:  nil,
			want: slog.LevelWarn,
		},
		{
			name: "plain error defaults to Warn",
			err:  errors.New("boom"),
			want: slog.LevelWarn,
		},
		{
			name: "ErrSagaStaleLease → Info (expected handoff race)",
			err:  errcode.New(errcode.KindConflict, errcode.ErrSagaStaleLease, "stale"),
			want: slog.LevelInfo,
		},
		{
			name: "wrapped ErrSagaStaleLease → Info (unwraps via errors.As)",
			err:  fmt.Errorf("drive: %w", errcode.New(errcode.KindConflict, errcode.ErrSagaStaleLease, "stale")),
			want: slog.LevelInfo,
		},
		{
			name: "ErrSagaNotFound → Warn (instance disappeared)",
			err:  errcode.New(errcode.KindNotFound, errcode.ErrSagaNotFound, "missing"),
			want: slog.LevelWarn,
		},
		{
			name: "ErrSagaDuplicateInstance → Warn (producer-side race, default)",
			err:  errcode.New(errcode.KindConflict, errcode.ErrSagaDuplicateInstance, "dup"),
			want: slog.LevelWarn,
		},
		{
			name: "unrelated errcode (ErrInternal) → Warn",
			err:  errcode.New(errcode.KindInternal, errcode.ErrInternal, "infra"),
			want: slog.LevelWarn,
		},
	}
	for _, tc := range cases {
		got := journalErrLevel(tc.err)
		if got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestFoldErrLevel(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		err  error
		want slog.Level
	}{
		{
			name: "ErrSagaFoldUnknownKind → Error (code↔schema drift, real anomaly)",
			err:  errFoldUnknownKind(0), // any kind value; classification is by code
			want: slog.LevelError,
		},
		{
			name: "wrapped ErrSagaFoldUnknownKind → Error (unwraps via errors.As)",
			err:  fmt.Errorf("drive: %w", errFoldUnknownKind(0)),
			want: slog.LevelError,
		},
		{
			name: "errFoldEventMismatch → Warn (defensive guard, expected)",
			err:  errFoldEventMismatch("inst-1", "KindStepFailed in history"),
			want: slog.LevelWarn,
		},
		{
			name: "plain error → Warn",
			err:  errors.New("boom"),
			want: slog.LevelWarn,
		},
	}
	for _, tc := range cases {
		got := foldErrLevel(tc.err)
		if got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}
