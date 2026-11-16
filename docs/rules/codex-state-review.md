# Project state after the verification fixes

Reviewed November 15, 2026, at `94fcf43`, including `fd1c8b5`. This updates the verdict in `codex-v3-fix-verification.md`.

The previously reported engine blockers are closed. I accept the three verification fixes and the telemetry wording correction. The standalone core has its agreed feature set, and this review found no new engine blocker. It is ready to move into integration and performance validation. That is not production sign-off.

## Independently checked

| Check | Result |
| --- | --- |
| `go test ./pkg/hooks/ -race -count=1` | Passed in 92.743 seconds. Only the separate soak was skipped |
| Original v3 and verification regressions | Passed in the ordinary suite. All 11 formerly gated failures now run without gates |
| Fresh hookctl, scenarios | 489 passed, zero failed or skipped |
| Fresh hookctl, documentation checks | 126 passed, zero failed or skipped |
| Golden engine column | 656 passed, 89 explicit exclusions, 14 raw outliers |
| Golden raw HTTP with mock upstreams | 656 passed, 30 explicit exclusions; 2,295 expectations checked |
| `TestClientCannotSetTheCLIFlag` | Passed over the wire |

Commands used `GOPROXY=off`. The golden selection was `-run '^TestGolden$/(engine|cells)/raw/mock|^TestClientCannotSetTheCLIFlag$'`, under the race detector. SDK and reference-server columns were not rerun in this review.

The new 154-case grid covers 22 hooks with matching and nonmatching route, flag and entitlement conditions, plus audit behavior. Sampled cases assert the output as well as a hit. This is substantially better evidence than the earlier tool-heavy suite. It is not every possible combination of those dimensions.

## Fixes accepted

The call resolver now retains the exposed item and the original item from the request. Tool, prompt and resource input/progress hooks reuse it. Resource result hooks find the original metadata for each returned URI. The original failing tests pass unchanged apart from gate removal.

Two additional independent probes passed:

- replacing tool rules after forwarding preserves that call's exposed and original names in the result hook
- when a resource read returns content under another listed URI, the result deny sees that other resource's original title despite a title modification

The empty-set assertion now rejects a missing result. The stream flush is deferred, preserving the consumer panic while reporting unknown records once. The code and Obsidian note no longer classify filtered server text as necessarily low-sensitivity.

The audit's task-handle fix applies before ordinary prompt/read processing, including directory reads through the shared read reply path. The engine's `clientCan` gate now suppresses CLI elicitation regardless of the supplied client map. Public MCP query parsing accepts only `ClientFlags`. The actual CLI/API entry point remains unbuilt, as the owner noted.

The mock SSE fix correctly prefixes each data line. The new metadata check compares the engine decision with what the mock received. Timeout cases observe cancellation and contrast it with a successful reply before the deadline. These improve the wire evidence; reference-server forward records still have the limitations recorded in the audit.

## One remaining test weakness

Three success cases have no positive assertion that the client received a result:

| File | Case and step |
| --- | --- |
| `pkg/hooks/testdata/e2e/mock-upstream-view.json` | hidden pinned parameter, step 1 |
| `pkg/hooks/testdata/e2e/mock-upstream-view.json` | rename reversal, step 1 |
| `pkg/hooks/testdata/e2e/mock-shape.json` | convert then clamp, step 0 |

They combine `client refused: false` with exact forwarded arguments. A client transport failure is not a gateway refusal under the new operator's definition, so it satisfies the first assertion.

I evaluated the pinned-parameter case's complete expectation list against a synthetic report containing the correct forwarded arguments and `client.error = {"code": 0, "message": "SDK timeout"}`, with no result. Every assertion passed.

This does not invalidate the proof of the forwarded arguments or establish an engine failure. It means those checks do not prove client success. Add a positive `client.result` assertion, preferably exact expected content, to those three steps. The curated real-call decision oracle also remains unfinished. Neither point reopens the fixed item-model blocker.

## Soak and remaining work

The current soak process is running. Its log starts at 20:51 UTC. At the 22:37 UTC sample it had completed 278,044 operations in 1 hour 46 minutes, with zero recorded failures. That sample reports a 103 MiB heap, 112 goroutines and 279 pending confirmations. This is an interim observation, not a completed six-hour result or a GC assessment. The soak README records a start time but does not pin a commit or source hash; retain that provenance with the final results.

Next work is completing the soak and reviewing allocation churn, live heap and GC under the intended load. Production integration still needs the gateway adapter, trusted request context, policy/catalog snapshots, the planned shared confirmation store, and transport behavior. Search of the production `internal/` and `cmd/` Go sources found the hooks import only in `cmd/hookctl`; the new engine remains standalone.

Tasks and other deferred extensions stay outside core completion. Old handover counts predate the latest commit; use the run counts above for this snapshot.

No engine code, tests or fixtures were changed during this review. The extra probes are in `/tmp/bifrost-codex-state-probes.go`. Raw outputs are `/tmp/bifrost-codex-state-race.jsonl`, `/tmp/bifrost-codex-state-golden.jsonl`, `/tmp/bifrost-codex-state-scenarios.log` and `/tmp/bifrost-codex-state-docchecks.log`. No commit or push was made.
