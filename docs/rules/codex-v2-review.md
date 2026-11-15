Codex verification of the v2 report — November 15, 2026

V2 fixes many of the reproduced v1 failures, but it is not ready for sign-off. The existing suite passes independently. Four new regression tests fail, including a retained-catalog leak, a resource-reply panic and an audit-budget regression. The earlier assertion-runner weakness also remains.

This is a focused verification of [v2-report.md](v2-report.md), not a completed second audit of every hook or a completed performance study. No engine implementation was edited, committed, pushed or integrated. Real fixtures are unchanged. The hook documentation folder remains outside this review.

Verified results

- a freshly built hookctl passes all 208 scenarios, with zero failures and zero skips
- `go test ./pkg/hooks/ -race -count=1` passes in 29.939 seconds before the new regressions are added
- all 17 original Codex Go tests are unchanged from the recorded v1 SHA-256 and now pass
- reconstruction of the original 70 Codex scenarios matches the recorded v1 SHA-256; exactly five scenarios changed: CX-19, CX-30 and CX-41 through CX-43
- those five expectation changes correspond to reserved escapes, skipping dynamic-skill rewrites and URI ordering, respectively
- each of the four new Go regressions fails in three consecutive race-enabled runs, without a race-detector report
- no previously snapshotted implementation or fixture file changed during this verification

The headline test progress is real. It was not achieved by replacing the original Codex Go assertions. However, pass counts are limited to what the tests assert.

V2-F01 · High · The prepared-tool cache prevents catalog collection

Source: [tools.go:24](../../pkg/hooks/tools.go:24), [tools.go:36](../../pkg/hooks/tools.go:36), [tools.go:119](../../pkg/hooks/tools.go:119).

The report says prepared tools are cached for the original tool's lifetime and cleaned up when it is collected. The global cache instead holds a strong reference chain:

```text
prepCache -> preparedTool -> tool
```

The weak map key does not break that chain. The original tool remains reachable, so its cleanup cannot remove the entry. Successive catalog generations retain their tools, schemas, descriptions and prepared data indefinitely through this cache.

Proof: V2-CX-G01, `TestCodexV2PreparedCacheReleasesCatalog`, in [the new regression file](../../pkg/hooks/codex_v2_review_test.go). It lists 32 tools, releases the catalog and result, then forces ten garbage collections. All 32 weak references still resolve. The test also gives those tools 4 MiB of aggregate description payload; that is fixture payload, not a measured total retained-heap estimate. It removes only its own cache entries at test cleanup.

Suggested fix: prefer prepared data owned by an immutable catalog generation, with the generation retained only by the cache owner and in-flight requests. If retaining a weak-key cache, its values must not retain the key referent directly or indirectly. Pass the original tool separately when needed. Keep property sharing and request lifetime guarantees. Test repeated catalog replacement and retained heap before tuning GOGC or GOMEMLIMIT. Lower allocation counts do not establish bounded live memory.

V2-F02 · High · Unsupported templates panic during unrelated resource replies

Source: [visibility.go:189](../../pkg/hooks/visibility.go:189), [resources.go:484](../../pkg/hooks/resources.go:484).

An unsupported template has `re == nil`, but a visible unsupported template is still placed in `okTemplates`. `owned()` calls `t.re.String()` without checking it. Resource-result handling constructs this ownership information even with no result rules and on a direct route.

Proof: V2-CX-G04, `TestCodexV2UnsupportedTemplateDoesNotPanicOnExactRead`. The catalog lists `file:///doc` and an unrelated template `file:///x{?q}`. Reading the exact resource forwards successfully. Handling its normal text reply panics with a nil pointer dereference. The regression recovers the panic only to report it as a test failure.

Suggested fix: include only supported nonnil matchers in the grant/rewriting ownership data. Preserve unsupported templates separately for conservative deny matching. Do not turn an unsupported template into a guessed grant. Cover direct and mux replies, with and without slugUris rules.

V2-F03 · High · Interleaved audit work still expires enforcement

Source: [eval.go:425](../../pkg/hooks/eval.go:425).

Separate elapsed-time counters were added, but `runBudget` also caches a `context.WithTimeout` for each mode. Once the first enforce rule creates that context, its wall-clock deadline keeps running while an audit rule executes. A later enforce rule reuses the expired context even though its own accounted allowance remains.

Proof: V2-CX-G02, `TestCodexV2InterleavedAuditCannotExpireEnforcement`. The control has two non-denying enforcement rules and passes. Inserting an audit rule between them causes the later enforce evaluation to fail with `operation interrupted: context deadline exceeded`. The test uses a controlled 250 ms delay in the audit-only field resolver, a 100 ms enforcement allowance and a one-second audit allowance. This isolates the accounting defect from CPU-speed-dependent helper workloads.

Suggested fix: create a fresh evaluation context from the remaining allowance for each rule, or rebuild the context whenever returning to a mode after other-mode work. Do not reuse a wall-clock deadline across excluded time. Preserve parent cancellation. Recheck aggregate allowance between evaluations, including parallel execution. The original CX-G06 passing does not cover this enforce/audit/enforce ordering.

V2-F04 · Medium · Composition disables explicit additional-property enforcement

Source: [tools.go:649](../../pkg/hooks/tools.go:649).

The report discloses this as weak spot 4, but later calls composed-schema handling agreed. The final comments required actual keyword semantics; they did not approve bypassing an explicit restriction whenever a composition keyword exists.

Proof: V2-CX-G03, `TestCodexV2ClosedSchemaStillClosesWithEmptyAllOf`. The schema declares `properties.q`, `additionalProperties: false` and `allOf: [{}]`. The valid `q` control forwards. An extra client key also forwards, with only an issue saying enforcement was skipped. The empty allOf branch imposes no extra constraint and does not weaken the top-level closed-object restriction.

Suggested fix: evaluate additionalProperties at its actual schema location, respecting properties and patternProperties there. An unrelated composition keyword must not disable that check. For genuinely unsupported transformations or nested validation, return a clear unsupported-enforcement error rather than claiming the restriction was enforced. Preserve valid composed schemas and avoid replacing JSON Schema semantics with a flat allowlist. This is enforcement of the advertised contract, not a reason to reject valid JSON wholesale.

V2-F05 · Medium · Empty-list assertions still accept error responses

Source: [scenario.go:605](../../pkg/hooks/scenario.go:605).

The CG01 reproduction from [the coverage audit](codex-coverage-gaps.md) still passes through the freshly built v2 CLI. It receives only `error: {code: -32004, message: "unknown tool missing"}`, but assertions that the missing `outcome.result.tools` has length zero and does not contain a secret pass.

Suggested fix: length checks must require a present collection of the intended type. Negative wildcard checks need a present parent collection and an expected successful outcome. Add runner regression tests for missing, null, empty and wrong-type values, without treating them as interchangeable. Strengthen success scenarios to assert their result kind and absence of errors. The v2 report's omission of this earlier finding does not resolve it.

Remaining evidence and reporting gaps

The new `TestRealCallsPolicyOracle` is a useful start, but [v2_test.go:43](../../pkg/hooks/v2_test.go:43) constructs one synthetic tool and four inline calls. It does not load the real call examples. It also classifies every nonnil Result as refused and defaults a missing outcome to error. Rename it accurately or load selected real fixtures; assert the actual error code, isError value, forwarded target and complete arguments. Its comment about clamping values above 100 conflicts with its explicit expectation that 250 is rejected before the clamp.

The byte-consistency test marshals `out.Result` without first requiring a successful nonnil result. Two failures with nil results would both serialize as `null`. Assert success and the expected result population first. It exercises tool ordering, not resource/template/skill ordering.

The report records the revised URI ordering, but the authoritative v3 design still says slug then name in its implementation decisions. Update the design and catalogue with adopted ordering and dynamic-skill restrictions. Keep the report's useful distinction between supported template matching and conservative unsupported-template denial.

I did not independently rerun the report's other-package suite, timed fuzz campaigns or benchmark table in this pass. The race run exercises fuzz seeds only. The measured allocation reductions, even if reproduced, would not establish healthy GC behavior because V2-F01 retains abandoned catalog generations. Performance and GC sign-off must include catalog turnover and retained heap after the cache lifetime is fixed.

Reproduction and artifacts

The new tests are [codex_v2_review_test.go](../../pkg/hooks/codex_v2_review_test.go). The suite now deliberately fails on these four new regressions. Existing implementation and expectations were not changed.

```sh
go test ./pkg/hooks -race -run '^TestCodexV2' -count=3 -v
go build -o /tmp/bifrost-codex-hookctl-v2 ./cmd/hookctl
/tmp/bifrost-codex-hookctl-v2 run -summary pkg/hooks/testdata/scenarios/*.json
/tmp/bifrost-codex-hookctl-v2 run /tmp/bifrost-codex-assertion-coverage.json
```

Raw evidence is in `/tmp/bifrost-codex-v2-race.jsonl`, `/tmp/bifrost-codex-v2-scenarios.txt` and `/tmp/bifrost-codex-v2-regressions-final.txt`. The file snapshot is `/tmp/bifrost-codex-v2-start-hashes.json`. The assertion probe is reproduced in full in the coverage audit so it can be recreated without temporary files from this session.

Fix the retained-cache ownership, nil matcher and interleaved deadline first. Then correct the explicit schema-enforcement gap and the test runner before treating another green suite as sign-off. Elicitation KV persistence and replay remain deferred as agreed.
