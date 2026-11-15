Bifrost specification coverage gaps — November 14, 2026

Yes: parts of the specification remain unproven by the current tests. Some catalogue entries assert more than their tests establish. Other requirements correctly remain deferred to gateway integration. Passing the standalone suite does not establish end-to-end MCP conformance.

This is an assertion and scope review, not a line-coverage measurement. It supplements [the correctness review](codex-review.md) and [final implementation comments](codex-final-comments.md). No engine code or existing expectations were changed. Performance work remains paused.

CG01: empty and negative assertions can pass on missing output

This is a newly verified runner weakness. [Expect.check](../../pkg/hooks/scenario.go:546) treats a missing value as length zero. A negative containment assertion also passes when the target is absent. Consequently, an RPC error can satisfy expectations intended to demonstrate a successful empty listing.

I ran this isolated probe through the existing hookctl binary:

```json
{
  "name": "coverage probe: error must not prove empty successful listing",
  "servers": [{"slug": "a", "catalog": {"tools": []}}],
  "steps": [{
    "op": "tools/call",
    "params": {"name": "missing"},
    "expect": [
      {"path": "outcome.result.tools", "len": 0},
      {"path": "outcome.result.tools[*].name", "notContains": "secret"}
    ]
  }]
}
```

The report says `status: pass`, although the sole outcome is `error: {code: -32004, message: "unknown tool missing"}`. No result or tools array exists. The probe is saved at `/tmp/bifrost-codex-assertion-coverage.json`.

Fix the runner so collection-length checks require a present collection of the intended type. For wildcard and negative assertions, require the parent collection to exist, or pair the assertion with explicit parent presence and outcome checks. Success scenarios should assert no RPC error and the expected result/forward kind before testing content. This does not establish that every current empty-list test passes incorrectly; it establishes that those assertions alone cannot prove their advertised behavior.

CG02: policy replacement is not exercised by its named test

[RH-CALL-08](../../pkg/hooks/testdata/scenarios/03-reversal-calls.json:109) changes `now` while keeping the same compiled policy. It proves time-dependent reevaluation, not that replacing a policy affects the next call.

Add a test that compiles and installs a genuinely different rule set between requests. Assert that the old exposed name fails, the new name reverses correctly, and newly denied operations never produce a Forward. Separately test a request paused across a policy/catalog replacement against the agreed snapshot semantics. The latter overlaps the explicitly deferred ADV-05 and ADV-25 integration work.

CG03: real-call policy coverage has no expected authorization decisions

[TestRealCallExamplesUnderPolicy](../../pkg/hooks/hooks_test.go:434) counts forwarded, refused and error outcomes. It fails only when all outcome categories are empty. A regression that refuses every real call could therefore pass this test.

The separate no-rule round-trip test is useful and should remain. Add a curated policy fixture with explicit expected decisions, exact forwarded names and arguments, and exact denial categories. It need not annotate all 936 examples to provide meaningful policy coverage. Keep the broad corpus loop labelled as a smoke test.

CG04: schema-reference transformation remains unproven

[ADV-45's test](../../pkg/hooks/hooks_test.go:150) aliases native Go maps. It contains no JSON Schema `$ref`, `$defs` or recursive reference. It proves immutability of shared maps, not the reference-aware behavior described in [ADV-45](adversarial-codex.md).

Use two branches referencing the same definition, then remove a field from only one branch. Assert that the other branch still permits and requires its original fields. Add a recursive reference case and require either a correct transformation or a clear unsupported-transformation error. There must be no dangling reference, global edit or expansion loop. Valid untouched schemas must still pass through.

The planned `additionalProperties: false` work also needs `patternProperties` and composed-schema cases. The final comments include a concrete valid input that a simple property-name allowlist would wrongly reject.

CG05: several freshness claims stop at supplied objects

These scenarios provide useful local checks, but do not prove fetching or transport behavior:

| Claim | What the test establishes | Missing evidence |
| --- | --- | --- |
| RH-SRV-07: instructions fetched live | One discover uses fixed text `version two` | Change a real fake-upstream response between two requests and count both fetches |
| RH-FRS-03 and RH-RSC-10: content/results never cached | Repeated requests produce forwarding descriptions | Count actual upstream calls and return different content on the second call |
| RH-FRS-04: metadata stripped at fetch | The scenario setup explicitly calls StripMeta before evaluation | Fetch-boundary stripping on cold fetch, cache reuse and refresh, before rule evaluation |
| RH-FRS-02: no change notifications | Discover advertises disabled capability flags | Observe the transport and verify no forbidden notification is emitted |

RH-SRV-07 is at [05-server-prompts.json:39](../../pkg/hooks/testdata/scenarios/05-server-prompts.json:39). The freshness scenarios start at [07-search-fresh-limits.json:38](../../pkg/hooks/testdata/scenarios/07-search-fresh-limits.json:38). Scenario metadata stripping is in [scenario.go:260](../../pkg/hooks/scenario.go:260).

These need an upstream spy and eventually the gateway adapter. They should not be labelled proven transport behavior by the standalone library.

CG06: client compatibility is not established through the rules engine

The scenario runner calls Go methods and compares selected JSON paths. It does not independently validate every response against the MCP schema or run the rules engine through multiple client implementations.

There is a useful [Go SDK wire test](../../internal/e2e/upstream/skills/sdkclient_test.go:47), but it talks directly to the Skills fixture server. It does not place the hooks engine or gateway in the path. Passing that test does not prove clients can consume the engine's shaped replies.

For v2, validate representative complete wire responses against the protocol contract, including required discriminators, cache fields, optional state, capabilities and content unions. At integration, run a client-to-gateway-to-fake-upstream flow. Cover absent capabilities and protocol-valid declarations, ordinary complete results, input_required with and without state, and supported content forms. A small representative client matrix is useful; claiming support for all clients would exceed the evidence.

CG07: timeout and cancellation claims have limited scope

[ADV-51](../../pkg/hooks/testdata/scenarios/07-search-fresh-limits.json:113) is named for zero or negative timeouts but supplies only zero. The positive test checks the string `1m30s` in Forward; it does not demonstrate cancellation of upstream work.

Add negative, overflow and valid-then-invalid override cases. At integration, hold an upstream request open and assert dispatch cancellation and handling of late replies. ADV-50 already records that lifecycle work as deferred.

The request-budget test exercises an expensive CEL filter. It does not prove every operation or custom helper stops promptly. CX-G06, CX-G07 and CX-G08 have since added stronger evidence and found failures. After those fixes, add repeated-helper and aggregate-audit budget checks; retain parent-cancellation checks independently.

CG08: identity-scoped caching and dispatch remain integration gates

The catalogue explicitly defers 16 ADV cases, including identity-dependent catalogs, shared fetches, pagination, refresh publication, dispatch races and connection reuse. See [the deferred list](test-cases.md:285). These are material authorization requirements, not optional performance tests.

The highest-value integration cases are:

- two identities with different upstream catalogs must not share an incorrectly scoped cache or in-flight fetch
- a denied skill ancestor on page two must still block a cold read
- failed refreshes must not publish a partial catalog as complete
- catalog or policy replacement between lookup and dispatch must not change the authorized target
- `?slugs=` restrictions, trusted caller identity and connection capabilities must survive the adapter boundary
- progress must return only on the originating permitted stream; task switches must affect actual dispatch

The existing engine tests operate on catalogs and caller maps already supplied to them. They cannot prove the gateway supplies or dispatches them correctly.

CG09: real fixtures are optional, so green does not imply corpus coverage

[realTools](../../pkg/hooks/hooks_test.go:269) skips on any fixture-loading error, not only a missing file. The scenario runner also supports skips. A green run can therefore omit real-corpus checks, and a damaged fixture may be reported as unavailable.

Keep an optional lightweight developer suite if useful. Add a designated corpus job that requires fixtures, checks their expected identity or counts, fails on parse/load errors, and reports skips explicitly. Never claim a real-corpus result from an all-skipped run. The original review's recorded run had the fixtures and no scenario skips.

CG10: fuzzing and concurrency results have specific boundaries

The URI fuzzer checks panic resistance and prefix/split round trips. It calls normalization functions without asserting authorization equivalence. Its 30,137 executions do not prove folder-deny or template-matching correctness. Add properties tied to actual authorization decisions, including exact-resource plus template-deny overlap and the agreed unsupported-template behavior.

The stronger CX-G05 concurrency test does establish isolation for its distinct entitlement renames, pins and reversal over a shared catalog. It does not exercise concurrent catalog publication, policy replacement or enabling profiling while requests are running. Those require their own lifecycle contracts and tests. Do not dilute the evidence already obtained by calling concurrency wholly untested.

What is already proven broken rather than merely unproven

The review now has direct failing tests for property-level copy-on-write through ListTools, effective-argument errors, several list/read disagreements, partial skill rewrites, valid protocol reply shapes and budget behavior. Those are implementation defects with regression evidence, not missing-test speculation.

Conversely, CX-G02 uses a genuinely issued token and proves generation-change rejection for that tested flow. It closes the earlier weak forged-token test's evidentiary gap. Elicitation persistence, replay and accumulated-answer handling stay with the agreed KV work; they are not reopened here.

Before claiming v2 meets the specification

Require exact decision and output assertions for the corrected library behavior. Fix the assertion weakness before interpreting new green scenario counts. Report passing, failing, skipped and deferred cases separately. Keep transport and storage requirements marked unproven until integration tests exercise them. The most useful evidence is a requirement mapped to an assertion that fails when that requirement is violated, not the presence of an RH or ADV identifier in a test name.
