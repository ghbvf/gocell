package sessionlogin_test

// handler_oracle_test.go verifies that the login handler does NOT expose
// password length constraints in error messages (length oracle prevention).
//
// This test was moved here from generated/contracts/http/auth/login/v1 to
// satisfy the CODEGEN-CONTRACT-USER-OVERLAP-01 archtest (no hand-written
// .go files under generated/contracts/). It exercises the generated handler
// as a black box via the public package API.
//
// This test was written RED (before B4 fix) to lock the security requirement:
// POST with a short password must return 400 but must NOT reveal "too short",
// "too long", or any specific length/range in the response body.

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	logingen "github.com/ghbvf/gocell/generated/contracts/http/auth/login/v1"
)

// stubLoginService satisfies the generated Service interface for oracle
// tests. It should never be called — the handler must reject invalid input
// before reaching service code.
type stubLoginService struct{}

func (s *stubLoginService) Login(_ context.Context, _ *logingen.Request) (*logingen.Response, error) {
	return nil, nil
}

// oracleErrorBody decodes a 400 response body and returns the error message.
func oracleErrorBody(t *testing.T, body []byte) string {
	t.Helper()
	var envelope struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(body), &envelope); err != nil {
		t.Fatalf("oracle: cannot decode error body %q: %v", string(body), err)
	}
	return envelope.Error.Message
}

func TestLoginHandler_ShortPassword_NoLengthOracle(t *testing.T) {
	h := logingen.NewHandler(&stubLoginService{})

	body := `{"username":"alice","password":"short"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/access/sessions/login", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected HTTP 400, got %d (body: %s)", w.Code, w.Body.String())
	}

	msg := oracleErrorBody(t, w.Body.Bytes())

	// These strings reveal the minimum password length and must NOT appear.
	lengthOracleStrings := []string{
		"too short",
		"too long",
		"minimum",
		"maximum",
		"minLength",
		"maxLength",
		"must be at least",
		"must be longer",
		"characters",
		"length",
		"8", // the actual minimum must not be leaked
	}
	for _, oracle := range lengthOracleStrings {
		if strings.Contains(strings.ToLower(msg), strings.ToLower(oracle)) {
			t.Errorf("password length oracle leak: response message %q contains %q", msg, oracle)
		}
	}
}

func TestLoginHandler_LongPassword_NoLengthOracle(t *testing.T) {
	h := logingen.NewHandler(&stubLoginService{})

	// 200-char password exceeds the 72-char maxLength.
	longPwd := strings.Repeat("a", 200)
	body := `{"username":"alice","password":"` + longPwd + `"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/access/sessions/login", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected HTTP 400, got %d (body: %s)", w.Code, w.Body.String())
	}

	msg := oracleErrorBody(t, w.Body.Bytes())

	oracleStrings := []string{
		"too short", "too long",
		"minimum", "maximum",
		"minLength", "maxLength",
		"72",  // the actual max must not be leaked
		"200", // or computed length
	}
	for _, oracle := range oracleStrings {
		if strings.Contains(strings.ToLower(msg), strings.ToLower(oracle)) {
			t.Errorf("password length oracle leak: response message %q contains %q", msg, oracle)
		}
	}
}
