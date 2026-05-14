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
		" to endpoints.subscribers in the contract.yaml;" +
		" fix: add subscribers to endpoints.subscribers or set lifecycle: deprecated"

	// ADV-06: contract lists a cell as subscriber but no matching contractUsage found.
	advHintADV06ContractToSlice = "event contract %q lists cell %q as subscriber," +
		" but no slice in %q declares contractUsage{contract: %q, role: subscribe};" +
		" add this contractUsage to a slice in %q" +
		" (e.g. cells/%s/slices/<slice>/slice.yaml)" +
		" or remove %q from endpoints.subscribers;" +
		" fix: add the subscribe contractUsage to a slice or remove the cell from endpoints.subscribers"

	// ADV-06: slice declares subscribe usage but contract does not list the cell.
	advHintADV06SliceToContract = "slice %q declares contractUsage{contract: %q, role: subscribe}," +
		" but the contract's endpoints.subscribers does not list cell %q;" +
		" add %q to the contract's endpoints.subscribers" +
		" or remove the subscribe contractUsage from this slice;" +
		" fix: add the cell to endpoints.subscribers in the contract or remove the subscribe contractUsage"

	// CH-04: auth.Mount correlation failed; cannot extract handler status codes.
	advHintCH04CorrelationFailed = "CH-04: contract %s served by handler file %s" +
		" — auth.Mount correlation failed; cannot reliably extract handler status codes." +
		" Required: handler file must register routes via" +
		" `auth.Mount(mux, auth.Route{Contract: spec, Handler: http.HandlerFunc(h.handleX)})`" +
		" pattern with a resolvable spec var or inline ContractSpec literal;" +
		" fix: ensure routes are registered via auth.Mount with a resolvable contract spec"

	// CH-05: auth.Mount correlation failed for UUID pathParam.
	advHintCH05CorrelationFailed = "CH-05: contract %s with `pathParams.{name}.format: uuid`" +
		" — auth.Mount correlation failed; cannot verify ParseUUIDPathParam call within handler function." +
		" Required: handler must use `auth.Mount(mux, auth.Route{Contract: spec, Handler: http.HandlerFunc(h.handleX)})` pattern;" +
		" fix: register routes via auth.Mount with a resolvable contract spec"

	// CH-05: pathParam with format:uuid missing ParseUUIDPathParam call.
	advHintCH05MissingParseCall = "%s: pathParam %q has format:uuid but handler does not call httputil.ParseUUIDPathParam(w, r, %q);" +
		" fix: add httputil.ParseUUIDPathParam call in the handler"

	// FMT-13: HTTP contract must declare endpoints.http.
	advHintFMT13MissingHTTP = "HTTP contract %q must declare endpoints.http; fix: add endpoints.http to contract.yaml:\n" +
		"  http:\n" +
		"    method: GET                # GET|POST|PUT|PATCH|DELETE\n" +
		"    path: /api/v1/...\n" +
		"    successStatus: 200\n" +
		"    noContent: false"

	// FMT-13: path placeholder has no pathParams declaration.
	advHintFMT13MissingPathParam = "http contract %q path placeholder %q has no pathParams declaration; fix: add to contract.yaml:\n" +
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
		" triggers must reference kind:event contracts;" +
		" fix: change the trigger to point at an existing kind:event contract id"

	// CCE-01: trigger event contract owner/publisher mismatch.
	advHintCCE01OwnerMismatch = advHintCCE01TriggerPrefix +
		" but event contract owner/publisher must both be %s;" +
		" fix: set ownerCell and endpoints.publisher on the event contract to the trigger contract's owner cell"

	// CCE-01: serving slice does not declare role:publish for the trigger event contract.
	advHintCCE01SliceNotPublish = advHintCCE01TriggerPrefix +
		" but serving slice %s/%s does not declare role: publish" +
		" for that event contract;" +
		" fix: add contractUsages: {contract: <event id>, role: publish} to the serving slice's slice.yaml"

	// CCE-01: trigger topic not found in slice emit set.
	advHintCCE01TriggerNotEmitted = advHintCCE01TriggerPrefix +
		" but no non-test Go file under %s emits it" +
		" via outbox.Emit or *.Emitter.Emit;" +
		" serving slice %s/%s must emit the trigger topic" +
		" as a string literal or named constant;" +
		" fix: add an outbox.Emit (or Emitter.Emit) call publishing the trigger topic in the slice's handler"

	// CCE-01: reverse — slice emits a topic not covered by any HTTP contract trigger.
	advHintCCE01ReverseEmit = "service emits topic %q in serving slice %s/%s" +
		" but no HTTP contract served by that slice declares it in triggers;" +
		" fix: add %q to the slice's HTTP contract triggers" +
		" or change the emit if dead code"

	// CCE-01: dynamic topic detected in a helper emit call — topic arg must be const/literal.
	advHintCCE01DynamicTopicHelper = "dynamic topic in helper emit not allowed;" +
		" topic argument must resolve to a string literal or named constant at %s:%d:%d;" +
		" fix: replace the dynamic topic expression with a string literal or a package-scope string constant"

	// CCE-01: dynamic topic detected in an outbox.Emit call — topic must be const/literal.
	advHintCCE01DynamicTopicEmit = "dynamic topic in emit not allowed;" +
		" topic must be string literal or named constant at %s:%d:%d;" +
		" fix: replace the dynamic topic expression with a string literal or a package-scope string constant"

	// CCE-01: dynamic EventType in receiver emit — EventType must be const/literal.
	advHintCCE01DynamicTopicReceiver = "dynamic topic in receiver emit not allowed;" +
		" EventType must resolve to a string literal or named constant at %s:%d:%d;" +
		" fix: replace the dynamic EventType expression with a string literal or a package-scope string constant"

	// DOC-NAME-01: guard file is absent.
	advHintDOCNAME01GuardRequired = "document naming guard is required for strict validation;" +
		" fix: create the guard file at " + docNamingGuardRelPath +
		" with include, exclude, and replacements fields"

	// DOC-NAME-01: guard file cannot be read.
	advHintDOCNAME01CannotReadGuard = "cannot read document naming guard: %v;" +
		" fix: ensure the guard file at " + docNamingGuardRelPath + " is readable"

	// DOC-NAME-01: guard file cannot be parsed.
	advHintDOCNAME01CannotParseGuard = "cannot parse document naming guard: %v;" +
		" fix: ensure the guard file at " + docNamingGuardRelPath + " is valid YAML"

	// DOC-NAME-01: guard file has no include patterns.
	advHintDOCNAME01MissingInclude = "document naming guard must declare at least one include pattern;" +
		" fix: add an include list to " + docNamingGuardRelPath

	// DOC-NAME-01: guard file has no replacements.
	advHintDOCNAME01MissingReplacements = "document naming guard must declare at least one replacement;" +
		" fix: add a replacements list to " + docNamingGuardRelPath

	// DOC-NAME-01: a replacement entry is missing literal or replacement field.
	advHintDOCNAME01InvalidReplacement = "document naming guard replacement requires literal and replacement;" +
		" fix: ensure every replacements entry has both literal and replacement fields"

	// DOC-NAME-01: cannot walk an include directory.
	advHintDOCNAME01CannotWalk = "cannot walk document naming include %q: %v;" +
		" fix: ensure the include path exists and is readable"

	// DOC-NAME-01: invalid glob pattern in include.
	advHintDOCNAME01InvalidPattern = "invalid document naming include pattern %q: %v;" +
		" fix: correct the glob pattern in the include list"

	// DOC-NAME-01: cannot read an active document.
	advHintDOCNAME01CannotReadDoc = "cannot read active document: %v;" +
		" fix: ensure the file exists and is readable"

	// DOC-NAME-01: active document contains a legacy literal.
	advHintDOCNAME01LegacyLiteral = "active document contains legacy literal %q; use %q;" +
		" fix: replace the legacy literal with the approved replacement"

	// DOC-NAME-01: cannot scan an active document.
	advHintDOCNAME01CannotScan = "cannot scan active document: %v;" +
		" fix: ensure the file is valid UTF-8 text"
)
