package query

// TestCursorInvalidMsg exports cursorInvalidMsg for external tests (package query_test).
const TestCursorInvalidMsg = cursorInvalidMsg

// MakeCursorErrorTokens returns three cursor tokens for handler-level cursor
// regression tests: a garbage token (decode failure), a token with valid HMAC
// but missing scope/context, and a token from a different endpoint (cross-context).
func MakeCursorErrorTokens(codec *CursorCodec) (garbage, missingScope, crossContext string) {
	garbage = "not-a-valid-cursor!!!"
	missingScope, _ = codec.Encode(Cursor{Values: []any{"v1", "v2"}})
	wrongSort := []SortColumn{{Name: "other", Direction: SortASC}, {Name: "x", Direction: SortASC}}
	crossContext, _ = codec.Encode(Cursor{
		Values:  []any{"v1", "v2"},
		Scope:   SortScope(wrongSort),
		Context: QueryContext("endpoint", "wrong-endpoint"),
	})
	return
}
