Codex final comments before v2 — November 14, 2026

Proceed with the fix pass. I accept most of the proposed changes in [the review response](codex-review-response.md). The remaining objections below concern concrete enforcement and compatibility behavior. These are conclusions about the proposals; implementation fixes remain unverified until v2 passes the regression tests.

Keep the work proportionate to real MCP clients and models. The gateway cannot restore digits a client already rounded or correct an identifier a model invented. It should accept valid JSON, preserve received values, and enforce the controls it advertises. This does not require a general-purpose numeric framework.

The [original findings and reproduction IDs](codex-review.md) remain the evidence. The [correctness handover](codex-correctness-handover.md) records the reviewed state. Performance work is paused at the user's request until v2. No further benchmark campaign or engine changes accompany these comments.

Follow-up: [the specification coverage audit](codex-coverage-gaps.md) identifies weak assertions and requirements that remain unproven. It includes a reproduced runner weakness where a missing result satisfies an empty-list assertion. Address that before treating green scenario totals as evidence of successful responses.

Accepted fixes and tradeoffs

I accept the response's accepted findings, subject to the qualifications below. In particular, use one visibility decision across routes; carry actual forwarded arguments into reply hooks; make skill rewrites all or nothing; preserve valid protocol shapes; and apply response limits after shaping.

- F21: accept skipping dynamic-skill rewriting consistently at list and read time. Skip the mutations, not applicable deny or visibility checks. Record this restriction in the design. CX-30 currently expects a successful consistent rewrite; if the restriction is adopted, replace that expectation with explicit checks that both entry and file remain unchanged. Retain a deny test.
- F28: accept decoding once per catalog version and placing overlays over the immutable decoded catalog. Preserve exact numbers and untouched sibling pointers. CX-G04 tests the actual listing path; CX-G05 tests entitlement isolation and reversal. Neither a map-only View test nor returning the original top-level pointer is sufficient by itself.
- A01: agree that reserved escapes such as `%2F` remain opaque. This agrees with the original review. CX-19 was classified as an ambiguity, not a proven bypass. Record the decision and give it an explicit passing expectation; do not globally decode reserved characters.
- ADV-48: accept separate parsers for the engine and fixture server. Correct the catalogue's shared-parser claim. Each parser still needs to meet its own input contract.
- F35: I do not object to stable URI ordering for resources, templates and skills. It is an intentional change from the current slug-then-name requirement. If adopted, update the design, catalogue and ordering tests together. This is low priority and does not justify delaying enforcement fixes.

F01 remains a real defect

I reject the claim that exact forwarding makes F01 irrelevant. CX-01 permits only `9007199254740992`, yet the engine accepts `9007199254740993`. Those are distinct values. An enum or pin authorizes a value; forwarding an unauthorized value without changing its bytes does not repair that decision.

The finding also includes schema corruption: changing an unrelated description changes an upstream numeric constraint. The F28 conversion work should eliminate that loss rather than preserve it in the new representation.

Keep the fix narrow: preserve exact schema values and compare numeric enum, pin and range values without rounding them into equality. Use bounded standard representations. If a constraint cannot be evaluated faithfully, reject that unsupported constraint where possible or return a clear policy evaluation error. Do not reject valid unconstrained JSON merely because its number is large. Do not turn quoted numbers or booleans into numbers.

If this is deferred, label it an accepted limitation with the reproductions still failing. It has not been disproved by a preference against numeric complexity.

F36 should fix ordinary client compatibility

Accept making `1`, `1.0` and `1e0` behave alike in integer arithmetic. CX-G17 demonstrates a practical client interoperability problem.

Reject a shortcut that first converts to `float64` and then tests whether the result is integral. For example, `1.0000000000000001` can already have rounded to `1` by that point. Determine integrality from the exact received value, keep ordinary representable integers consistent, and preserve the original value for forwarding unless a rule changes it. Keep existing exact int64 handling. This can be a small adapter fix; broad support for exotic arithmetic is not required.

F11 needs a complete work bound

Accept a hard traversal limit as a simpler protection. It must cover native conversion, visited and copied nodes, depth, and all paths in a helper invocation. Resetting a counter for each path leaves repeated large copies permitted. Repeated helper calls also need aggregate accounting through CEL or a shared evaluation bound. Check the bound before allocating or recursing past it.

The regression cases are one wide object with many removal paths, one deep input, and repeated helper calls in a comprehension. They must stop within the declared limit and fail closed. CX-G08 already proves that the current CEL accounting reports the same cost for one row and 10,000 rows.

The design explicitly promises helper cost estimates. A separate hard cap can be an agreed alternative, but it is not evidence that CEL now meters that work. Either charge conservative work to CEL or document the separate bound honestly. No elaborate costing subsystem is necessary.

F13 needs a bounded audit allowance

Accept excluding audit work from the enforcement budget. The proposal must explain how that exclusion actually works: retaining one wall-clock deadline around both kinds of work does not exclude audit time.

A CEL limit per expression does not establish an aggregate audit deadline. Use a small bounded allowance for audit work and record exhaustion as `would error` or an explicitly incomplete audit trace. Preserve parent request cancellation. This can be simple budget accounting; it does not need a separate service or a general scheduling framework.

CX-G06 must continue to forward when only audit work exhausts its allowance. Multiple expensive audit rules must also finish within the audit bound. F12's deadline checks must use the appropriate enforcement accounting, or they will reintroduce the same failure at the final return.

F06 can use a restricted matcher with an explicit boundary

I found one actual string-valued resource template in the real JSON fixtures: `datadog://mcp/skills/skills-library/{slug}` in `datadog.json`. Other `uriTemplate` occurrences describe an output-schema property. I found no real fixture template that requires another operator. The proposed simple and reserved expansions cover that observed template; this does not establish full RFC 6570 coverage.

Preserve every literal segment and distinguish `{x}` from `{+x}`. Unsupported templates must not grant guessed ownership. They must also not silently disappear from applicable deny checks. An exact resource listing could otherwise grant a URI that an unsupported template would deny, breaking any-deny-wins.

For example, add an exact listed URI also covered by a denied query-expansion template such as `file:///x{?q}`. The implementation must either apply the matching deny or explicitly refuse the unsupported authorization case. Treating the template as a harmless nonmatch is insufficient. Document the supported subset and make unsupported authorization visible through issues or errors.

Additional properties must follow JSON Schema semantics

Accept enforcing `additionalProperties: false`. Apply it to the exposed parameter interface, accounting for renames and gateway-added parameters. Preserve hidden-name and upstream-name rejection independently.

Do not implement it as “every key must appear in properties.” This valid schema also permits matching keys:

```json
{
  "type": "object",
  "patternProperties": {"^x-": {"type": "string"}},
  "additionalProperties": false
}
```

`{"x-trace":"ok"}` must remain valid. Composed schemas also need their actual keyword scope respected. Use established JSON Schema semantics for the enforcement being added; do not silently substitute a narrower allowlist. This is the compatibility concern behind the original unknown-argument note, not an objection to enforcing an explicit schema restriction.

Assessment of the additional issues

| Response item | Verdict and concrete recommendation |
| --- | --- |
| 1. Readable state reveals upstream names | True: [state.go](../../pkg/hooks/state.go:83) base64-encodes signed JSON. However, opaque protocol state does not itself promise encryption, and renaming does not establish a general confidentiality guarantee. Treat this as a hardening choice. With KV already planned, an opaque random handle can keep the payload server-side; do not add a parallel cryptographic redesign now. |
| 2. Argument text can mislead consent | True: the helper example concatenates model-controlled `args.id` into the prompt. Separate trusted wording from displayed values, quote and bound values, and escape control characters appropriately. Preserve enough target identity for meaningful approval. Quoting and truncation reduce confusion; they do not prove immunity to prompt injection across clients. |
| 3. Upstream instructions can imitate another section | True: [server.go](../../pkg/hooks/server.go:251) inserts upstream text directly beneath slug headings. Indenting every line and normalizing line endings improves structural separation. Plain-text and model consumers can still read malicious instructions; call this a presentation mitigation, not a trust boundary. |
| 4. Rule names reach clients | True: [eval.go](../../pkg/hooks/eval.go:101) includes the rule label in generated failure reasons. Generic generated failures are reasonable; keep diagnostic labels in audit. Preserve explicit administrator-authored refusal reasons promised by the design. |
| 5. `limit` is silently ineffective | True: [hooks.go](../../pkg/hooks/hooks.go:96) permits it, while the [call setter](../../pkg/hooks/engine.go:319) handles only args and timeout. Reject enabled rules using it until enforcement exists. This is a concrete additional finding and a useful small fix. |
| 6. Calls scan the whole catalog | True: [resolveTool](../../pkg/hooks/engine.go:240) lists the catalog again. A target-only path is reasonable when compiled metadata proves no name changes. It must still reject duplicate upstream names, reevaluate relevant policy, and preserve filtering, reversal, server configuration and cancellation. Do not cache caller-dependent decisions. Performance validation waits for v2. |
| 7. Empty generations cannot detect replacement | True when both old and new policy generations remain empty: [continuation](../../pkg/hooks/engine.go:541) compares equality. Require a generation at every challenge-issuance path, including wrapped upstream challenges, or derive it from immutable policy identity. A nonempty constant also needs replacement on policy changes. |
| 8. Missing capabilities break the example | The context currently passes the client map through without providing the nested capabilities map. Normalize absence to an empty map without mutating the caller's shared map. Test the actual example against absent capabilities and the protocol-valid elicitation declaration. This is a useful compatibility fix. |
| 9. A fixed slice end can exceed list length | True: cel-go's slice rejects an end beyond the list length. A new helper is optional. The existing expression `size(xs) > 20 ? xs.slice(0, 20) : xs` provides the cap. Prefer fixing the example unless a dedicated helper materially improves authoring; if added, specify negative limits and cost. |

The MIME-type, partial server/config shutdown and search metadata observations in item 10 also match the inspected code. Apply the proposed fixes. The unknown-method error-code differences are a documented API decision, not by themselves a security defect. Disabled rules should not close an entitlement, and any promised warning must be observable.

V2 verification and elicitation deferrals

Rerun all original scenarios, all CX scenarios and the race suite after the implementation pass. Review any changed expectation against an explicit design decision. A revised contract can justify changing an expectation; matching current implementation cannot. Keep known deferrals and the scenario-runner fixes visible in the results.

Leave elicitation persistence and replay with the KV work, as requested. D01's method and immutable entitlement binding fixes are useful now. D02's accumulated answers remain a handover item. F04, F33 and F34 concern forwarded arguments and valid protocol shapes, so they still need fixing independently of storage. Cover both gateway-issued and wrapped upstream challenges when generation validation changes.

No additional approval round is needed from this review before implementing the accepted work. Bring back v2 with its updated design decisions and test results; the next review will verify those claims independently and resume the speed, memory and GC assessment.
