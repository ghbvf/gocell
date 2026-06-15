package policymanage

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/tests/contracttest"
)

// Contract-level serve/publish tests for the policymanage slice. Each of the 6
// contractUsages in slice.yaml (5 HTTP serve + 1 event publish) is exercised
// against the real handler/producer output and validated with contracttest's
// schema validators (CONTRACT-PATH-QUERY-COVERAGE-01 + producer payload guard).
// The HTTP serve tests reuse setupPolicyHandler / withHandlerAdmin (handler_test.go);
// the event test reuses newDurableTestService (service_test.go).

// postCreatePolicy POSTs a minimal valid create body via h and returns the new
// policy id from the 201 response. Fails the test on any non-201.
func postCreatePolicy(t *testing.T, h http.Handler, name string) string {
	t.Helper()
	body := `{"name":"` + name + `","rules":[{"id":"r1","name":"Allow all","effect":"allow"}]}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/access/policies", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	h.ServeHTTP(w, withHandlerAdmin(req))
	require.Equal(t, http.StatusCreated, w.Code, "create must return 201: %s", w.Body.String())
	var resp struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.NotEmpty(t, resp.Data.ID)
	return resp.Data.ID
}

func TestContract_PolicyCreateV1_Serve(t *testing.T) {
	root := contracttest.ContractsRoot(t)
	c := contracttest.LoadByID(t, root, "http.policy.create.v1")
	h := setupPolicyHandler(t)

	// Happy path → 201, response validates against the contract schema.
	body := `{"name":"P","rules":[{"id":"r1","name":"N","effect":"allow"}]}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest(c.HTTP.Method, c.HTTP.Path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	h.ServeHTTP(w, withHandlerAdmin(req))
	c.ValidateHTTPResponseRecorder(t, w)

	// rowScope=all is valid wire vocabulary but rejected by policy authoring → 422.
	allBody := `{"name":"P","rules":[{"id":"r1","name":"N","effect":"allow","obligations":{"rowScope":"all"}}]}`
	wAll := httptest.NewRecorder()
	reqAll := httptest.NewRequest(c.HTTP.Method, c.HTTP.Path, strings.NewReader(allBody))
	reqAll.Header.Set("Content-Type", "application/json")
	h.ServeHTTP(wAll, withHandlerAdmin(reqAll))
	require.Equal(t, http.StatusUnprocessableEntity, wAll.Code, "rowScope=all must be 422: %s", wAll.Body.String())
	c.ValidateErrorResponse(t, http.StatusUnprocessableEntity, wAll.Body.Bytes())
}

// TestContract_PolicyCreateV1_CrossAttrFanout exercises the eq_attr / rhsSource /
// rhsKey HTTP fanout through the real generated handler + embedded request
// validator (F4, #1977). The happy path asserts the cross-attribute condition
// round-trips with its RHS preserved in the response; the malformed cases assert
// the schema-layer if/then/else rejects them with 400 BEFORE the converter —
// restoring the pre-eq_attr 400 status for static-condition shape errors (F3),
// instead of the domain validator's 422.
func TestContract_PolicyCreateV1_CrossAttrFanout(t *testing.T) {
	root := contracttest.ContractsRoot(t)
	c := contracttest.LoadByID(t, root, "http.policy.create.v1")

	serve := func(t *testing.T, body string) *httptest.ResponseRecorder {
		t.Helper()
		h := setupPolicyHandler(t)
		w := httptest.NewRecorder()
		req := httptest.NewRequest(c.HTTP.Method, c.HTTP.Path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		h.ServeHTTP(w, withHandlerAdmin(req))
		return w
	}

	// Happy path: eq_attr + rhsSource/rhsKey + no values → 201, response validates
	// against the contract schema AND preserves the RHS fields.
	t.Run("eq_attr_happy_201_rhs_preserved", func(t *testing.T) {
		body := `{"name":"CrossAttr","rules":[{"id":"r1","name":"Self ownership","effect":"allow",` +
			`"conditions":[{"source":"subject","key":"sub","operator":"eq_attr","rhsSource":"resource","rhsKey":"id"}]}]}`
		w := serve(t, body)
		require.Equal(t, http.StatusCreated, w.Code, "eq_attr create must be 201: %s", w.Body.String())
		c.ValidateHTTPResponseRecorder(t, w)
		var resp struct {
			Data struct {
				Rules []struct {
					Conditions []struct {
						Operator  string   `json:"operator"`
						RHSSource string   `json:"rhsSource"`
						RHSKey    string   `json:"rhsKey"`
						Values    []string `json:"values"`
					} `json:"conditions"`
				} `json:"rules"`
			} `json:"data"`
		}
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		require.Len(t, resp.Data.Rules, 1)
		require.Len(t, resp.Data.Rules[0].Conditions, 1)
		cond := resp.Data.Rules[0].Conditions[0]
		require.Equal(t, "eq_attr", cond.Operator)
		require.Equal(t, "resource", cond.RHSSource, "response must preserve rhsSource")
		require.Equal(t, "id", cond.RHSKey, "response must preserve rhsKey")
		require.Empty(t, cond.Values, "cross-attr condition must carry no values")
	})

	// Malformed shapes — all rejected by the schema layer at 400 (F3).
	cases := []struct {
		name string
		cond string
	}{
		{"static_missing_values_400", `{"source":"subject","key":"dept","operator":"eq"}`},
		{"static_carrying_rhsKey_400", `{"source":"subject","key":"dept","operator":"eq","values":["eng"],"rhsKey":"id"}`},
		{"eq_attr_with_values_400", `{"source":"subject","key":"sub","operator":"eq_attr","rhsSource":"resource","rhsKey":"id","values":["x"]}`},
		{"eq_attr_missing_rhs_400", `{"source":"subject","key":"sub","operator":"eq_attr"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := `{"name":"Bad","rules":[{"id":"r1","name":"Bad","effect":"allow","conditions":[` + tc.cond + `]}]}`
			w := serve(t, body)
			require.Equal(t, http.StatusBadRequest, w.Code, "malformed condition must be 400 (schema layer): %s", w.Body.String())
			c.ValidateErrorResponse(t, http.StatusBadRequest, w.Body.Bytes())
		})
	}
}

func TestContract_PolicyGetV1_Serve(t *testing.T) {
	root := contracttest.ContractsRoot(t)
	c := contracttest.LoadByID(t, root, "http.policy.get.v1")
	c.ValidatePathParam(t, "id", "pol-550e8400")
	c.MustRejectPathParam(t, "id", "") // violates minLength: 1

	h := setupPolicyHandler(t)
	id := postCreatePolicy(t, h, "GetMe")

	// 200 validates against the response schema.
	w := httptest.NewRecorder()
	req := httptest.NewRequest(c.HTTP.Method, strings.Replace(c.HTTP.Path, "{id}", id, 1), nil)
	h.ServeHTTP(w, withHandlerAdmin(req))
	c.ValidateHTTPResponseRecorder(t, w)

	// Unknown id → 404 error envelope.
	w404 := httptest.NewRecorder()
	req404 := httptest.NewRequest(c.HTTP.Method, strings.Replace(c.HTTP.Path, "{id}", "pol-ghost", 1), nil)
	h.ServeHTTP(w404, withHandlerAdmin(req404))
	require.Equal(t, http.StatusNotFound, w404.Code)
	c.ValidateErrorResponse(t, http.StatusNotFound, w404.Body.Bytes())
}

func TestContract_PolicyUpdateV1_Serve(t *testing.T) {
	root := contracttest.ContractsRoot(t)
	c := contracttest.LoadByID(t, root, "http.policy.update.v1")
	c.ValidatePathParam(t, "id", "pol-550e8400")
	c.MustRejectPathParam(t, "id", "") // violates minLength: 1

	h := setupPolicyHandler(t)
	id := postCreatePolicy(t, h, "UpdMe")
	path := strings.Replace(c.HTTP.Path, "{id}", id, 1)

	// 200 validates.
	updBody := `{"name":"Updated","rules":[{"id":"r1","name":"N","effect":"allow"}],"expectedVersion":1}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest(c.HTTP.Method, path, strings.NewReader(updBody))
	req.Header.Set("Content-Type", "application/json")
	h.ServeHTTP(w, withHandlerAdmin(req))
	c.ValidateHTTPResponseRecorder(t, w)

	// Wrong expectedVersion → 409 error envelope.
	conflictBody := `{"name":"X","rules":[{"id":"r1","name":"N","effect":"allow"}],"expectedVersion":99}`
	w409 := httptest.NewRecorder()
	req409 := httptest.NewRequest(c.HTTP.Method, path, strings.NewReader(conflictBody))
	req409.Header.Set("Content-Type", "application/json")
	h.ServeHTTP(w409, withHandlerAdmin(req409))
	require.Equal(t, http.StatusConflict, w409.Code)
	c.ValidateErrorResponse(t, http.StatusConflict, w409.Body.Bytes())

	// rowScope=all → 422 (converter rejects before the version check).
	allBody := `{"name":"X","rules":[{"id":"r1","name":"N","effect":"allow","obligations":{"rowScope":"all"}}],"expectedVersion":1}`
	w422 := httptest.NewRecorder()
	req422 := httptest.NewRequest(c.HTTP.Method, path, strings.NewReader(allBody))
	req422.Header.Set("Content-Type", "application/json")
	h.ServeHTTP(w422, withHandlerAdmin(req422))
	require.Equal(t, http.StatusUnprocessableEntity, w422.Code, "rowScope=all must be 422: %s", w422.Body.String())
	c.ValidateErrorResponse(t, http.StatusUnprocessableEntity, w422.Body.Bytes())
}

func TestContract_PolicyDeleteV1_Serve(t *testing.T) {
	root := contracttest.ContractsRoot(t)
	c := contracttest.LoadByID(t, root, "http.policy.delete.v1")
	c.MustRejectPathParam(t, "id", "") // violates minLength: 1

	// Per-param reject coverage (CONTRACT-PATH-QUERY-COVERAGE-01).
	c.ValidateQueryParam(t, "expectedVersion", "1")
	c.MustRejectQueryParam(t, "expectedVersion", "0")      // violates minimum: 1
	c.MustRejectQueryParam(t, "expectedVersion", "100000") // violates maximum: 99999

	h := setupPolicyHandler(t)
	id := postCreatePolicy(t, h, "DelMe")

	// 204 No Content on success.
	w := httptest.NewRecorder()
	req := httptest.NewRequest(c.HTTP.Method, strings.Replace(c.HTTP.Path, "{id}", id, 1)+"?expectedVersion=1", nil)
	h.ServeHTTP(w, withHandlerAdmin(req))
	require.Equal(t, http.StatusNoContent, w.Code)

	// Missing expectedVersion → generated handler rejects with 400.
	wBad := httptest.NewRecorder()
	reqBad := httptest.NewRequest(c.HTTP.Method, strings.Replace(c.HTTP.Path, "{id}", id, 1), nil)
	h.ServeHTTP(wBad, withHandlerAdmin(reqBad))
	require.Equal(t, http.StatusBadRequest, wBad.Code)
	c.ValidateErrorResponse(t, http.StatusBadRequest, wBad.Body.Bytes())
}

func TestContract_PolicyListV1_Serve(t *testing.T) {
	root := contracttest.ContractsRoot(t)
	c := contracttest.LoadByID(t, root, "http.policy.list.v1")

	// Per-param reject coverage (CONTRACT-PATH-QUERY-COVERAGE-01).
	c.ValidateQueryParam(t, "limit", "1")
	c.MustRejectQueryParam(t, "limit", "0")                        // violates minimum: 1
	c.MustRejectQueryParam(t, "limit", "501")                      // violates maximum: 500
	c.MustRejectQueryParam(t, "cursor", strings.Repeat("x", 4097)) // violates maxLength: 4096

	h := setupPolicyHandler(t)
	postCreatePolicy(t, h, "L1")
	postCreatePolicy(t, h, "L2")

	// Page 1 (limit=1) → 200 validates; hasMore + a signed nextCursor.
	w := httptest.NewRecorder()
	req := httptest.NewRequest(c.HTTP.Method, c.HTTP.Path+"?limit=1", nil)
	h.ServeHTTP(w, withHandlerAdmin(req))
	c.ValidateHTTPResponseRecorder(t, w)
	var page1 struct {
		Data       []json.RawMessage `json:"data"`
		NextCursor string            `json:"nextCursor"`
		HasMore    bool              `json:"hasMore"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &page1))
	require.Len(t, page1.Data, 1)
	require.True(t, page1.HasMore)
	require.NotEmpty(t, page1.NextCursor)

	// Page 2 via the signed nextCursor → 200 validates.
	w2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(c.HTTP.Method, c.HTTP.Path+"?limit=1&cursor="+url.QueryEscape(page1.NextCursor), nil)
	h.ServeHTTP(w2, withHandlerAdmin(req2))
	c.ValidateHTTPResponseRecorder(t, w2)

	// A tampered/garbage cursor is rejected by the signed codec → 400 (this is the
	// path the contract's "invalid cursor → 400" promise was unreachable on before
	// the codec was adopted, F5).
	wBad := httptest.NewRecorder()
	reqBad := httptest.NewRequest(c.HTTP.Method, c.HTTP.Path+"?cursor=not-a-valid-cursor", nil)
	h.ServeHTTP(wBad, withHandlerAdmin(reqBad))
	require.Equal(t, http.StatusBadRequest, wBad.Code, "tampered cursor must be 400: %s", wBad.Body.String())
	c.ValidateErrorResponse(t, http.StatusBadRequest, wBad.Body.Bytes())
}

func TestContract_EventPolicyUpdatedV1_Publish(t *testing.T) {
	root := contracttest.ContractsRoot(t)
	c := contracttest.LoadByID(t, root, "event.policy.updated.v1")

	svc, writer := newDurableTestService(t)
	_, err := svc.Create(testSvcAdminCtx(), CreateInput{Name: "EvtP", Rules: minimalRules()})
	require.NoError(t, err)
	require.Len(t, writer.Entries, 1, "Create must emit exactly one outbox entry")
	entry := writer.Entries[0]

	// The producer's real payload + envelope id validate against the contract.
	c.ValidatePayload(t, entry.Payload())
	c.ValidateHeaders(t, []byte(`{"eventId":"`+entry.ID()+`"}`))

	// Producer guard (unevaluatedProperties:false): a payload missing required
	// fields, or one leaking state-bearing fields (rule bodies) onto the bus, is
	// rejected by the schema.
	c.MustRejectPayload(t, []byte(`{"policyId":"x"}`))
	c.MustRejectPayload(t, []byte(`{"policyId":"p","version":1,"action":"created","actorId":"a","rules":[]}`))
}
