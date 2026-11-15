# Verification of the v3 review response

Reviewed November 15, 2026, on `rules-hooks`, at `9a7cb39`. The fixes are in `ef55dcc`.

The original policy fixes are accepted. The claim that every reported issue is fully closed needs two qualifications: unknown-record sensitivity and consumer-panic cleanup. A newly found item-model defect still lets valid denies miss after metadata changes. Fix that before integrating the engine.

This review checked the response against the implementation, the Obsidian v3 design and its November 15 follow-ups, hook documentation, and the test runner. `flags.cli` is not built yet, as the owner clarified. Its implementation is outside this verdict. Client SDK coverage, the KV deployment, and performance work remain with their existing owners.

## Original findings

| Original finding | Verdict | Evidence and qualification |
| --- | --- | --- |
| V3-F01 bare resource metadata | Accepted | `item.resolve` handles bare resources before defaults. Read input and progress carry `CallContext.Bare`. Original G01/G02 pass. This closes the bare-resource defect; the separate listed-item defect below remains |
| V3-F02 prefixless denied template | Accepted | `denies` covers the empty prefix; routing uses the separate `mayCover`. Original G03 passes |
| V3-F03 native scheme fallback | Accepted | Only `not_found` falls through. Ambiguous and unavailable fallback outcomes survive. Original G04 and its scenario cases pass |
| V3-F04 second final after cleanup panic | Accepted | `finalSent` is set before yielding. Cleanup faults are reported without another final. Original G05 passes |
| V3-F05 shaped progress size | Accepted | The serialized rewrite gets the size check. Original G06 passes |
| V3-F06 public listing panic | Accepted | Public `Set.ListTools` recovers and lists nothing. Engine callers retain full fault reporting through private `listTools`. Original G08 passes |
| V3-F07 customer data in shape records | Mitigation accepted; classification rejected | The email-key regression passes. Filtering useful names is a reasonable tradeoff. It cannot establish that retained text is low-sensitivity; see the wording change below |
| V3-F08 missing progress fault | Accepted | The first rule panic is reported immediately and carried on the final outcome. Original G07 passes |
| V3-F09 lost unknown records | Lost-reply fix accepted; closure claim incomplete | Original G10 passes, as does an ordinary early consumer return. A consumer panic still skips the callback; V3F-F03 below |

The original 13 Go tests run without their old gates. Their assertions were not weakened. The 20 moved scenarios are structurally identical after removing exclusion metadata.

## Verification results

| Check | Result |
| --- | --- |
| `GOPROXY=off go test ./pkg/hooks/ -race -count=1` | Passed, 74.208 seconds. The soak and another agent's scratch audit were skipped |
| Fresh CLI, ordinary scenarios | 298 passed, zero failed or skipped |
| Fresh CLI, documentation checks | 126 passed, zero failed or skipped |
| Golden engine column | 465 passed, 85 explicitly excluded, 14 raw outliers outside the engine column |
| Golden raw HTTP against mock upstreams | 461 passed, 30 explicitly excluded; 1,796 expectations checked |
| New focused probes with `HOOKS_KNOWN_GAPS=1` | 29 subtests: 18 passed, 11 failed, grouped into three findings below |
| Final run of original v3 tests and new controls | Passed under race: 42 leaf tests passed, 11 known failures skipped |

The golden selection was `-run '^TestGolden$/(engine|cells)/raw/mock'`, under the race detector. It did not run SDK cells or raw reference-server cells. No race report occurred. These runs are evidence of the stated checks, not a claim that the suite cannot be flaky.

New probes are in `pkg/hooks/codex_v3_fix_review_test.go`. The 11 failing checks are opt-in using the existing `knownGap` helper. Passing controls run normally. Every retained failure asserts an explicit contract; none depends on a disputed JSON coercion or client restriction. An exploratory `prompts/result` probe was discarded: that hook is not part of the registry, so it was a test mistake, not an engine finding.

## V3F-F01 High severity item fields change meaning across hooks

The design's “Item model” says current fields reflect earlier modifications, while `upstream.*` remains the original item. It explicitly recommends `upstream.name` for a rule that must survive a rename. The tool result, input and progress pages also describe top-level fields as the exposed tool.

The implementation breaks both halves of this contract:

| Surface | Source locations | Observed behavior | Reproduction |
| --- | --- | --- | --- |
| Tool result, input and progress | `pkg/hooks/server.go:369`, `pkg/hooks/stream.go:409` | Both top-level fields and `upstream` come from the original tool. After `t` becomes `public`, a deny on `name == 'public'` misses | V3F-G03, three failures |
| Prompt input and progress | `pkg/hooks/resources.go:228`, `pkg/hooks/stream.go:94`, `pkg/hooks/items.go:46` | A new view uses the exposed prompt as its base, making `upstream.name` equal `public`. A deny on the original name misses | V3F-G04, two failures |
| Listed resource read, result, input and progress | `pkg/hooks/resources.go:511`, `pkg/hooks/resources.go:848`, `pkg/hooks/resources.go:918`, `pkg/hooks/stream.go:110` | The exposed resource becomes the view's upstream base. Changing its title changes what `upstream.title` reads. A deny against the original title misses | V3F-G05, four failures |

A minimal tool policy is valid JSON:

```json
[
  {"hook":"tools/modify","when":"true","then":{"name":"'public'"}},
  {"hook":"tools/result","when":"name == 'public'","then":{"decision":"'deny'"}}
]
```

Calling `public` correctly invokes upstream `t`. Its sensitive result then reaches the caller because the result hook reads `name == 't'`. Input requests and progress show the same error. Controls using `upstream.name == 't'` deny correctly, establishing that those hooks run.

For a listed resource, change its title from `Original` to `Public`, then deny reads on `upstream.title == 'Original'`. The read forwards. The same condition fails to block resource results, input requests and progress. Prompt renames similarly defeat original-name rules in their input and progress hooks.

Suggested fix: preserve both the original item and its exposed view through the call context. Resolve ordinary fields from the exposed view and `upstream` from the original. Reuse one resolver across the call and reply paths. Carry existing pointers or overlays; do not rerun modify rules to reconstruct the view. Resource result items need their own original catalog entry, since a response can contain a different URI. Preserve the bare-resource rule: an unlisted URI has no invented metadata.

Add combined modify-then-call/input/result/progress tests for each supported family. Assert both current and original fields in the same rule. This is a newly discovered coverage gap; the evidence does not attribute its introduction to `ef55dcc`.

## V3F-F02 Medium severity an empty-set assertion accepts no result

Location: `pkg/hooks/scenario.go:946`. Reproduction: V3F-G02, `TestCodexV3FixEmptySetNeedsAList/error_instead_of_listing`.

The golden README says `set` means “a list has exactly these elements, in any order.” This assertion passes against a JSON-RPC error with no result:

```json
{"path":"client.result.tools","set":[]}
```

`checkMore` rejects a non-list only when the path exists. A missing value then becomes a nil slice, which compares equal to the empty expected set. A broken listing can therefore satisfy an empty-list expectation.

Suggested fix: require the resolved value to be a list before comparing its elements. Keep explicit absence assertions separate. Wildcard projections also need a parent-presence check if they must distinguish an empty source list from a missing source.

The new probe proves the false positive with a complete error response. Controls accept a present empty array and reject null and a nonempty array. No existing golden assertion currently uses `set: []`, so this finding does not invalidate a counted current pass. It is a defect in the new assertion operator.

## V3F-F03 Low severity a consumer panic loses unknown records

Locations: `pkg/hooks/stream.go:152` and `pkg/hooks/stream.go:180`. Reproduction: V3F-G01, `TestCodexV3FixUnknownFlushOnConsumerExit/consumer_panic`.

The response says every stream ending flushes records once. An upstream emits an unknown notification, then progress. The consumer panics while handling that progress. The engine correctly preserves the exact consumer panic and closes the upstream, but `OnUnknown` is never called.

The nested recovery re-panics while `inYield` is true. That exits the iterator before its final, ordinary call to `ns.flush()`.

Suggested fix: defer the once-only flush immediately after constructing `notifications`. Keep consumer panics unchanged and do not synthesize a final response. The passing early-return control proves the ordinary departure path already works.

## Accepted telemetry tradeoff needs accurate wording

Keep the filtering. Change the sensitivity claim in `pkg/hooks/unknown.go:27` and the Obsidian unknown-record follow-up.

`fieldName` accepts an alphabetic customer name such as `AliceSmith`. `safeTarget` at `unknown.go:132` preserves URI paths and authorities. A path such as `file:///customers/AliceSmith/diagnosis` survives unchanged. Removing queries and fragments does not make the remaining target low-sensitivity.

Suggested replacement: “Records omit payload values and filter some names. Names and URI targets can still contain sensitive customer data. Send them to the access-controlled diagnostic destination used for customer data.”

If a general-purpose destination requires no such data, omit or hash arbitrary names and targets there. The engine can continue accepting valid JSON and exposing useful diagnostics through the protected callback. This is a classification correction, not a demand for a perfect heuristic.

## Flags and test coverage

The additional flag compiler checks passed. They cover absolute identifiers, `has`, comprehensions, alias attempts, indexing and local shadowing with named fields. Existing route-normalization and flag/schema tests also passed. Inspection found search mode selecting presentation behavior; it does not supply an internal authorization grant.

`flags.cli` remains deferred. When built, derive it from the gateway API entry point, and declare no MCP elicitation capability for that path. Do not treat an MCP query parameter as that trusted assertion. The harness query parsing is not a verdict on an unbuilt production path.

The generated hook schemas are useful, but their tests establish variable names and declared types, not the identity of the runtime values. V3F-F01 is the missing behavioral proof. Individual rename tests and individual hook tests cannot establish that modifications and original metadata survive between hooks.

The weak real-call oracle and engine-derived `forwardMeta`/timeout assertions remain as acknowledged in the response. Their suggested fixes remain exact decision/forward expectations on curated real calls, and assertions against independently observed transport behavior. This review found no reason to reopen the settled client-size or numeric-handling decisions.

Earlier Obsidian paragraphs still conflict with later decisions. Mark them superseded, especially listing-required reads and task support. The retained failures above follow the current item model and follow-ups; they do not depend on those old paragraphs.

## Reproduce and hand over

```sh
# Existing v3 tests and new passing controls; known failures are skipped.
GOPROXY=off go test ./pkg/hooks/ -race -count=1 -run '^TestCodexV3'

# Eleven failing subtests; each should pass after the corresponding fix.
HOOKS_KNOWN_GAPS=1 GOPROXY=off go test ./pkg/hooks/ -race -count=1 -run '^TestCodexV3Fix' -v

# The reviewed golden columns.
GOPROXY=off go test ./pkg/hooks/harness/ -race -count=1 -run '^TestGolden$/(engine|cells)/raw/mock'
```

Implement V3F-F01 first, then tighten `set` and defer the stream flush. Remove the relevant `knownGap` gates after each fix. Correct the telemetry wording separately.

This review added only this report and `pkg/hooks/codex_v3_fix_review_test.go`. No engine implementation or real fixture was edited. No commit or push was made. Another agent's scratch audit and new `97-gap-audit.json` were left untouched.

Returning to elicitation: the prior concurrent-redemption test still passes, with one accepted continuation and 31 refusals. The new input-hook failures concern metadata used by policy, not the KV-store plan. Keep the storage work on its existing track.
