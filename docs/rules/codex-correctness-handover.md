Bifrost correctness review handover — November 14, 2026

The adversarial review is complete. The engine needs corrections before gateway integration. This handover describes the reviewed state; it does not claim later edits have been verified.

The full findings, evidence and suggested fixes are in [codex-review.md](codex-review.md). Machine-readable results and reviewed file hashes are in [codex-review-results.json](codex-review-results.json). Use those hashes to distinguish this review from concurrent edits on the uncommitted `rules-hooks` branch. The recorded HEAD is `1cf47283d48619c93c8f37f932564c16f4af512d`.

The review added [70 scenarios](../../pkg/hooks/testdata/scenarios/90-codex-adversarial.json) and [17 Go tests plus three fuzzers](../../pkg/hooks/codex_adversarial_test.go). It did not change engine implementation, upstream server implementation or real fixtures. It did not commit, push or integrate anything into the gateway. The actively edited `docs/rules/hooks/` folder was excluded.

Verified results:

- the original race suite passed in 33.769 seconds before review tests were added
- all 119 original scenarios still pass
- the 70 added scenarios have 13 passes and 57 failures: 53 engine deviations, two KV-deferred failures, one runner defect and one design ambiguity
- the 17 added Go tests have four passes and 13 failures
- the stronger parallel entitlement, pin and reversal isolation test passes
- URI fuzzing completed 30,137 executions without a detected violation of its tested invariants
- the argument and rules parser fuzzers fail on deterministic trailing-JSON seeds
- the upstream Skills server race suite passes
- no race report or flaky result was observed in the checks performed

The combined suite now fails intentionally because the regression tests assert required behavior. Do not change expectations to make the current implementation pass. There are 36 findings, not 57 independent scenario bugs. Each finding states the requirement, observed behavior, proof IDs, source locations and a suggested fix.

Prioritize authorization and integrity: exact JSON number handling; effective arguments and reversal; resource, template and directory visibility; skill rewrite integrity; enforcement limits; protocol-shaped replies; and the profiling nil-Set panic. The report also identifies weak test assertions and one runner defect. Keep implementation fixes separate from changes to the runner or agreed design.

JSON compatibility is a requirement. Preserve valid number spellings such as `1`, `1.0` and `1e0`, exact large values, valid array/scalar/null structured content, and untouched composed or boolean schemas. F36 shows that equivalent integral JSON values currently take different CEL arithmetic paths. Rejecting valid client JSON is not the proposed solution. F01 and F36 describe exact-value handling and defined errors where CEL cannot represent a value.

Elicitation storage and replay policy are moving to a KV store at the user's direction. D01 and D02 remain regression evidence for that work, with no request to redesign the current in-memory implementation. F04 still concerns the arguments actually forwarded upstream. F33 and F34 concern valid MCP message shapes. Those bugs are independent of storage and remain in scope.

Reproduce from the repository root:

```sh
go build -o bin/hookctl ./cmd/hookctl
./bin/hookctl run -summary pkg/hooks/testdata/scenarios/*.json
./bin/hookctl run -summary pkg/hooks/testdata/scenarios/90-codex-adversarial.json
go test ./pkg/hooks/ -race -count=1
go test ./pkg/hooks/ -race -count=1 -run '^TestCodex'
go test ./internal/e2e/upstream/skills/ -race -count=1
```

The design from `# v3: Hooks` onward is authoritative: `/home/localuser/vaults/Datadog/AI Platform/AI Gateway/MCP Gateway - Policy Engine Intent.md`. The review report links the test catalogue, earlier adversarial review, MCP 2026-07-28 schema and Skills specification. The excluded hook documentation was not used to overrule the design.

Next work is a separate performance review of timing instrumentation, allocation churn, live heap, concurrency and GC. Run benchmarks with `-run '^$'` to avoid mixing known failing correctness tests into performance measurements. Preserve snapshot isolation, list/call agreement, exact JSON values and fail-closed behavior in every optimization.

Update: the user paused the performance review pending v2. The first existing-benchmark run completed; its raw output is `/tmp/bifrost-codex-existing-bench.txt`, and the starting file hashes are `/tmp/bifrost-codex-perf-start-hashes.json`. These preliminary timings are not a completed memory or GC review. No benchmark process remains running. [Final comments on the implementation response](codex-final-comments.md) record the accepted proposals, remaining objections and v2 verification requirements.
