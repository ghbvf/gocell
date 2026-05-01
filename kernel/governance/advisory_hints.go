package governance

// advisory_hints.go centralizes long developer-facing hint messages used by
// advisory rules. These hints are product value (greppable diagnostic strings),
// so the file is exempt from the lll linter via .golangci.yml exclusions.
//
// All const names follow the convention: advHint<RuleID><Suffix>.
//
// Constant promotion criteria — promote a literal string into this file when:
//   (a) it is referenced ≥ 3 times, OR
//   (b) any single instance exceeds ~80 characters, OR
//   (c) two or more rules share the same diagnostic vocabulary and would
//       otherwise drift out of sync independently.
// Sub-80-char single-use literals stay inline at the rule call site so the
// rule body remains self-explanatory.

const (
	// ADV-05: active event contract with no subscribers.
	advHintADV05EmptySubscribers = "event contract %q is active but has no subscribers;" +
		" mark lifecycle: deprecated or add at least one cell or actor" +
		" to endpoints.subscribers in the contract.yaml"

	// ADV-06: contract lists a cell as subscriber but no matching contractUsage found.
	advHintADV06ContractToSlice = "event contract %q lists cell %q as subscriber," +
		" but no slice in %q declares contractUsage{contract: %q, role: subscribe};" +
		" add this contractUsage to a slice in %q" +
		" (e.g. cells/%s/slices/<slice>/slice.yaml)" +
		" or remove %q from endpoints.subscribers"

	// ADV-06: slice declares subscribe usage but contract does not list the cell.
	advHintADV06SliceToContract = "slice %q declares contractUsage{contract: %q, role: subscribe}," +
		" but the contract's endpoints.subscribers does not list cell %q;" +
		" add %q to the contract's endpoints.subscribers" +
		" or remove the subscribe contractUsage from this slice"

	// CH-04: auth.Mount correlation failed; cannot extract handler status codes.
	advHintCH04CorrelationFailed = "CH-04: contract %s served by handler file %s" +
		" — auth.Mount correlation failed; cannot reliably extract handler status codes." +
		" Required: handler file must register routes via" +
		" `auth.Mount(mux, auth.Route{Contract: spec, Handler: http.HandlerFunc(h.handleX)})`" +
		" pattern with a resolvable spec var or inline ContractSpec literal."

	// CH-05: auth.Mount correlation failed for UUID pathParam.
	advHintCH05CorrelationFailed = "CH-05: contract %s with `pathParams.{name}.format: uuid`" +
		" — auth.Mount correlation failed; cannot verify ParseUUIDPathParam call within handler function." +
		" Required: handler must use `auth.Mount(mux, auth.Route{Contract: spec, Handler: http.HandlerFunc(h.handleX)})` pattern."

	// CH-05: pathParam with format:uuid missing ParseUUIDPathParam call.
	advHintCH05MissingParseCall = "%s: pathParam %q has format:uuid but handler does not call httputil.ParseUUIDPathParam(w, r, %q)"

	// FMT-13: HTTP contract must declare endpoints.http.
	advHintFMT13MissingHTTP = "HTTP contract %q must declare endpoints.http; add to contract.yaml:\n" +
		"  http:\n" +
		"    method: GET                # GET|POST|PUT|PATCH|DELETE\n" +
		"    path: /api/v1/...\n" +
		"    successStatus: 200\n" +
		"    noContent: false"

	// FMT-13: path placeholder has no pathParams declaration.
	advHintFMT13MissingPathParam = "http contract %q path placeholder %q has no pathParams declaration; add to contract.yaml:\n" +
		"  pathParams:\n" +
		"    %s:\n" +
		"      type: string"

	// advHintCCE01TriggerPrefix is the shared lead-in for CCE-01 messages
	// reporting trigger declaration mismatches on an HTTP contract. The four
	// CCE-01 hints below all begin with this prefix; the first %q is the
	// contract id and the second %q is the trigger topic.
	advHintCCE01TriggerPrefix = "contract %q declares trigger %q"

	// CCE-01: trigger references a contract that is not kind:event.
	advHintCCE01TriggerNotEvent = advHintCCE01TriggerPrefix +
		" but referenced contract kind=%s;" +
		" triggers must reference kind:event contracts"

	// CCE-01: trigger event contract owner/publisher mismatch.
	advHintCCE01OwnerMismatch = advHintCCE01TriggerPrefix +
		" but event contract owner/publisher must both be %s"

	// CCE-01: serving slice does not declare role:publish for the trigger event contract.
	advHintCCE01SliceNotPublish = advHintCCE01TriggerPrefix +
		" but serving slice %s/%s does not declare role: publish" +
		" for that event contract"

	// CCE-01: trigger topic not found in slice emit set.
	advHintCCE01TriggerNotEmitted = advHintCCE01TriggerPrefix +
		" but no non-test Go file under %s emits it" +
		" via outbox.Emit or *.Emitter.Emit;" +
		" serving slice %s/%s must emit the trigger topic" +
		" as a string literal or named constant"

	// CCE-01: reverse — slice emits a topic not covered by any HTTP contract trigger.
	advHintCCE01ReverseEmit = "service emits topic %q in serving slice %s/%s" +
		" but no HTTP contract served by that slice declares it in triggers;" +
		" fix: add %q to the slice's HTTP contract triggers" +
		" or change the emit if dead code"
)
