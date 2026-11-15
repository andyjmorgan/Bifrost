Bifrost rules engine v3 review — November 15, 2026

Not ready to wire into Bifrost. The normal suite passes, but independent regressions reproduce resource-policy bypasses, loss of mux routing errors, and stream lifecycle defects. The changed decisions are acceptable: reads may be unlisted, URIs stay unchanged, gateway confirmations use an atomic-take store, and older upstreams lose elicitation and progress. Those decisions are not the problem. Eight implementation findings below and one false telemetry-safety claim remain. Each has a failing regression and a proposed fix. Client-test work is still underway and is not the basis for this verdict.

Review scope and revision

I read the v3 handover, the rules documentation and earlier review/response documents, all hook pages, the authoritative Obsidian design from “v3: Hooks” through its follow-ups, the linked query/restrictions note, and the relevant multiplexing/cache ADR and addendum. The November 15 follow-ups take precedence over superseded paragraphs. I checked the local MCP 2026-07-28 schema for the affected URI, progress and result contracts. Skills documentation was read; skills implementation and extension behavior were excluded from this core review.

The review started at `34136a3` with uncommitted changes. HEAD advanced externally to `0d916d3` during the review, incorporating the principal, clock, read-cache and harness changes, and the new review tests/scenarios. I issued no commit or push commands. Final engine regressions and scenario runs were against that code. I changed only this new report, [the new Go tests](../../pkg/hooks/codex_v3_review_test.go), and [the new scenarios](../../pkg/hooks/testdata/known-gaps/96-codex-v3.json). Engine implementation and existing expectations were not edited by this review. Snapshot comparison found no changes to real fixtures. The root credential file was not read.

Validation

| Check | Observed result |
| --- | --- |
| Initial hooks race suite | pass, 76.37 seconds; only the opt-in soak skipped |
| Final hooks race suite | pass, 73.94 seconds; 96 top-level test/fuzz targets passed; the 10 deliberate v3 gaps and the soak skipped |
| Existing scenarios, fresh CLI | initial 267/267; latest 268/268; no skips |
| Documentation checks | 126/126; no skips |
| New v3 scenarios | 6 passed, 14 failed, no skips |
| New Go regressions with `HOOKS_KNOWN_GAPS=1` | 10 failed; 3 control tests passed; no race report |
| Scenario-loader fuzzing | 37,811 executions in about 11 seconds; no failure |
| Harness/parity race suite, initial run | 26 TypeScript leaf cases failed because the call never reached the gateway |
| Client adherence | passed; all 103 reported tests/subtests passed |
| Reference servers, race enabled | passed; all 91 reported tests/subtests passed; server executable package has no tests |
| Client/server matrix | passed; 1,055 reported tests/subtests passed and 45 skipped |

The client and server runtimes were already installed. No setup downloads were needed. Harness suites used `REFSERVERS_REQUIRE=all`. The client failure path was being changed concurrently: the newer driver captures SDK notes about unadvertised capabilities. At the owner's request I stopped pursuing client-suite defects. The initial parity result is a measurement of that earlier snapshot, not a claim that the later harness still fails. The skips and known client differences are not evidence of engine conformance.

Reproduce the new findings:

```sh
HOOKS_KNOWN_GAPS=1 go test ./pkg/hooks/ -race -count=1 -run '^TestCodexV3'
go build -o /tmp/bifrost-codex-hookctl-v3 ./cmd/hookctl
/tmp/bifrost-codex-hookctl-v3 run -summary pkg/hooks/testdata/known-gaps/96-codex-v3.json
```

Both commands that run the new gaps currently exit nonzero. The normal suite deliberately excludes these failures. No intermittent engine failure was observed in the runs performed; this was not a soak or repeated-flake investigation.

Findings, ranked by severity

V3-F01 — High: unlisted resources acquire invented metadata through some hook paths

Code: [items.go:31](../../pkg/hooks/items.go#L31), [resources.go:911](../../pkg/hooks/resources.go#L911), [stream.go:110](../../pkg/hooks/stream.go#L110).

The Obsidian follow-up at line 1327 says an unlisted URI has only `uri`. Reading `name`, `mimeType` or another missing field must fail and refuse. That is essential to making metadata-based denies safe on unlisted reads.

`item.resolve("upstream")` calls `derived` before checking `bare`. `derived` fills missing fields with empty defaults. Consequently, `upstream.name == 'secret'` evaluates false instead of failing. The read is forwarded under a filter/read deny; a result deny returns the sensitive content. The same problem keeps unlisted directory children and foreign-URI content that should have failed filtering. Separately, resource input and progress handlers reconstruct the item without `bare`, so even plain `name` silently defaults there.

Evidence: V3-G01 and V3-G02 in [codex_v3_review_test.go:26](../../pkg/hooks/codex_v3_review_test.go#L26). Scenarios V3-S01 through S09 and S20 fail. These include direct/mux reads, directory children, content returned under another URI, and an upstream question. V3-S17 and S18 are passing controls: a false URI guard permits the read, while an unguarded top-level missing `name` already refuses.

Fix: use one resource-item constructor/resolver for every hook. Carry whether the item was listed in the call context. For a bare item, expose only `uri` and an `upstream` map containing only `uri`; do not call the default-filling derivation. Preserve ordinary missing-key and presence-check semantics. Apply that resolver to filter, read, input, result and progress hooks. A failed progress rule should drop only its event.

V3-F02 — High: a denied unsupported template with an empty literal prefix denies nothing

Code: [visibility.go:74](../../pkg/hooks/visibility.go#L74).

The design says a denied unsupported template denies everything under its literal prefix. A valid RFC 6570 template can start with an expression. For example, `{+base}{?q}` expands to `file:///secret.txt?q=x` with suitable values.

The template is hidden by `templates/filter`, but `mayCover` requires `prefix != ""`. Its deny therefore covers no URI. The expansion is forwarded.

Evidence: V3-G03 in [codex_v3_review_test.go:74](../../pkg/hooks/codex_v3_review_test.go#L74), and V3-S10. V3-S15 confirms the nonempty-prefix case already denies correctly.

Fix: for the denial predicate, an empty literal prefix must conservatively cover every URI. Keep denial coverage separate from routing claims if a prefixless unsupported template should not claim every URI. Alternatively reject such a denied configuration explicitly; silently weakening the deny is not acceptable.

V3-F03 — High: native scheme resolution discards ambiguity and unavailable-catalog errors

Code: [resources.go:719](../../pkg/hooks/resources.go#L719).

The design separates routing from permission and requires ambiguity to be reported. Its native-scheme follow-up says to try the full URI when the apparent slug's server does not claim the remainder.

With a server named `git` and two other servers listing `git+ssh://host/repo`, fallback correctly finds two claimants. `route` keeps only successful fallback results and discards the ambiguity. It then forwards `ssh://host/repo` to `git`. If `git` does not exist, the ambiguity becomes an ordinary not-found error. An unavailable-catalog refusal is discarded in the same way.

Evidence: V3-G04 in [codex_v3_review_test.go:90](../../pkg/hooks/codex_v3_review_test.go#L90), and V3-S11 through S13. V3-S14 confirms a unique native-scheme claimant is handled correctly.

Fix: preserve fallback's ambiguity and unavailable outcomes, including its audit event and candidate list. Only a genuine no-claimant result should fall back to interpreting an existing explicit slug. Use a typed routing outcome rather than treating every non-nil error identically.

V3-F04 — Medium: a cleanup panic can produce two final responses

Code: [stream.go:158](../../pkg/hooks/stream.go#L158), [stream.go:185](../../pkg/hooks/stream.go#L185).

The stream contract permits notifications followed by one final response. Once sent, that response cannot be replaced.

An upstream iterator yields a successful final response and then panics in deferred cleanup when iteration stops. The engine has already yielded the first final. Recovery reports the fault and yields a second final refusal because it tracks consumer stop but not final emission.

Evidence: V3-G05 in [codex_v3_review_test.go:104](../../pkg/hooks/codex_v3_review_test.go#L104): two final events and one fault callback. This is an engine iterator defect; no particular SDK behavior is needed to reproduce it.

Fix: record that the final event has been emitted before invoking the consumer. Continue reporting cleanup faults, but emit a replacement refusal only if no final has been sent and the consumer has not stopped.

V3-F05 — Medium: rewritten progress bypasses the output size cap

Code: [stream.go:325](../../pkg/hooks/stream.go#L325).

The engine's configured limit bounds accepted and produced messages. Existing result paths check again after shaping. Progress checks the input length but returns newly marshaled output without another size check.

A small progress event becomes 4,143 bytes under a 1,024-byte limit. It is relayed; the final result still succeeds. This concerns the gateway's own cap, not the accepted Python event-size difference.

Evidence: V3-G06 in [codex_v3_review_test.go:127](../../pkg/hooks/codex_v3_review_test.go#L127).

Fix: check the serialized event after rewriting. On excess, drop and record that event, and let the final response proceed with its independent allowance. Use the same rule for all three progress hooks.

V3-F06 — Medium: the public Set listing method still lets a panic escape

Code: [tools.go:798](../../pkg/hooks/tools.go#L798), [state.go:62](../../pkg/hooks/state.go#L62).

The handover promises panic safety and specifically asks for exported `Set` methods to be checked. `Engine.ListTools` has recovery, but public `Set.ListTools` does not.

A JSON catalog containing one ordinary tool and a `null` entry produces a panic on both the single-worker and multi-worker paths. The parallel helper correctly moves a worker panic to the caller, but there is no public-method recovery there. A caller that invokes this API outside another recovery boundary can lose its goroutine/process. The current harness uses the protected Engine path; this is not a demonstrated remote crash through that harness.

Evidence: V3-G08 in [codex_v3_review_test.go:158](../../pkg/hooks/codex_v3_review_test.go#L158).

Fix: make the public method fail closed with an issue/fault and no partially accepted listing. Keep the unexported worker method free to propagate to the Engine boundary. If direct Set evaluation is not supported, remove that public entry point and document the supported Engine API.

V3-F07 — Medium, design claim: shape records are not guaranteed free of customer data

Code: [unknown.go:19](../../pkg/hooks/unknown.go#L19), [unknown.go:49](../../pkg/hooks/unknown.go#L49).

The design deliberately records field names and types, then concludes that records “never hold a value” and are safe to log and aggregate. The implementation follows the field-name requirement. The safety conclusion is false: payload keys can themselves be email addresses, identifiers or secrets.

An unknown content block with a synthetic customer email as a key puts that exact email in `OnUnknown` output. The 32-field cap does not sanitize keys or limit their length. `Name` and `Target` are also server-controlled strings, and a URI target can carry sensitive query data.

Evidence: V3-G09 in [codex_v3_review_test.go:180](../../pkg/hooks/codex_v3_review_test.go#L180). The test uses synthetic data only.

Fix: choose an explicit telemetry contract. For records safe for general aggregation, retain only known structural names and describe other keys by counts/types or bounded keyed hashes. Bound names, lengths and distinct groups. If exact unknown names are necessary for investigation, treat those records as potentially sensitive and restrict their destination/retention. Change the documentation's safety claim accordingly. This is a design/data-handling correction, not a JSON conformance defect.

V3-F08 — Low: progress-rule panics disappear from Fault reporting

Code: [stream.go:311](../../pkg/hooks/stream.go#L311), [stream.go:179](../../pkg/hooks/stream.go#L179), [fault.go:155](../../pkg/hooks/fault.go#L155).

A panic caught by CEL should produce a fault containing the rule and call, while a progress-rule failure drops only that event. The event is dropped correctly. However, the final handler's recovery/reporting runs before progress hits are appended. No later code promotes a progress `Hit.Panic` to a fault.

Evidence: V3-G07 in [codex_v3_review_test.go:142](../../pkg/hooks/codex_v3_review_test.go#L142): the final succeeds and includes a panic-marked hit, but `OnFault` is called zero times and `Outcome.Fault` is nil.

Fix: report a progress CEL panic at event evaluation, retaining the rule and call. Attach the fault to the eventual final outcome without refusing an otherwise valid final result. Bound/deduplicate repeat reports so a stream cannot flood fault logging.

V3-F09 — Low: lost streams discard collected unknown records

Code: [stream.go:59](../../pkg/hooks/stream.go#L59), [stream.go:168](../../pkg/hooks/stream.go#L168).

The unknown-record contract promises records on the outcome and through `OnUnknown`. A stream that sends an unknown notification then breaks returns the correct `-32014` lost-reply error, but loses both copies of the unknown record. Only the normal-final path flushes `ns.seen`.

Evidence: V3-G10 in [codex_v3_review_test.go:200](../../pkg/hooks/codex_v3_review_test.go#L200): zero outcome records and zero callback records after one unknown notification.

Fix: flush collected unknown records exactly once on every terminal path, including lost replies and recovered engine panics. Define callback behavior when the consumer stops early, since there may be no final outcome to attach them to.

New scenario accounting

All expectations describe the current design. None was changed to match the observed defect.

| IDs | Result | Assessment |
| --- | --- | --- |
| V3-S01 to S09, S20 | 10 failed | engine wrong: absent resource fields silently default in some hook paths |
| V3-S10 | failed | engine wrong: lost deny on a prefixless unsupported template |
| V3-S11 to S13 | 3 failed | engine wrong: fallback errors discarded |
| V3-S14 to S19 | 6 passed | controls for unique routing, nonempty template denial, unchanged URI bytes, guarded missing fields, unguarded denial, and unlisted returned content |

The new scenarios total 20. The prior 70 Codex scenarios remain in the normal suite. The 10 failing Go tests cover these cases and failures the scenario format cannot express. G09 identifies the telemetry design contradiction; the other failing Go tests identify implementation defects. No failed scenario depends on an unresolved design choice.

Claims checked and found true

- The previous v2 regressions pass: abandoned prepared catalogs are collectible, unsupported-template ownership no longer panics, audit work does not expire a later enforcement deadline, and explicit closed schemas remain closed through composition. The missing-value assertion regression also passes.
- Unlisted direct reads are allowed, and a false URI guard avoids a missing-field failure. URI escapes, query contents and Unicode are forwarded unchanged in V3-S16.
- A missing, unknown, older, or nonexact current protocol string cannot enable elicitation or progress; tasks remain off. Missing result discriminators become `complete`. V3-C03 constructs the engine directly, so the scenario runner cannot hide an absent version.
- Existing stream tests pass for early consumer exit, consumer-panic propagation, stopping after the final, final refusal after relayed progress, and a spent progress allowance leaving the final its own budget. The new cleanup regression shows the boundary those tests missed.
- Direct Config/Modify/Connect recovery is present; compile callers in the scenario builder and harness check problems instead of silently using the nil set as unrestricted. I found no externally exported way to fabricate the private budget marker through an ordinary gateway context.
- Real-corpus byte-order consistency and catalog immutability tests passed. So did the real-call round-trip test. These results do not turn the separate policy smoke test into an authorization oracle.
- The current clock fallback uses real time. The new opaque-principal behavior and confirmed-read `ttlMs: 0` change are present; their existing tests/scenarios pass.

Coverage that still proves less than its label

[The real-call policy test](../../pkg/hooks/hooks_test.go#L434) counts categories rather than asserting the correct decision for each fixture. [TestPolicyCallOracle](../../pkg/hooks/v2_test.go#L54) uses four synthetic calls, not the real corpus. Its “refused” classification accepts any non-nil result rather than requiring an error result with the intended reason. Fix the label/comment and add a small curated real-call oracle with exact forwards and refusals. Broad corpus execution remains useful as a smoke test.

[The scenario fuzzer](../../pkg/hooks/v2_test.go#L93) proves only panic resistance for the inputs it actually runs. It skips nil scenario entries, limits scenario size/counts, and does not check expected authorization decisions. The 37,811 executions are not proof of policy correctness or of hookctl accepting/rejecting every malformed scenario safely.

The existing bare-resource scenario checks a plain `name` read. It misses `upstream.name`, resource input, resource progress, directory children and sibling content. V3-G01/G02 and V3-S01 through S09/S20 fill those holes.

The existing stream panic tests cover a panic before the final and a panic in the consumer. They do not cover iterator cleanup after the final. The progress size test checks input size, not rewritten output. Unknown-record tests use harmless property names and normal completion. The new regressions establish why those distinctions matter.

The parity bridge correctly takes forwarded server, method and params from recorded upstream requests. However, [observedForward](../../pkg/hooks/harness/parity_test.go#L1018) copies `forwardMeta` and `timeout` from the engine outcome. Assertions on those fields cannot establish that a transport applied the decision. Intermediate client-side assertions are explicitly skipped when an SDK folds retries: the initial run reported 77 Go, 69 Python and 69 TypeScript such expectations. Raw HTTP reported zero. These omissions are visible in the counters, but a green SDK scenario is not proof of every expectation. This is recorded for the ongoing harness work, without expanding that review.

The current matrix handover acknowledges missing real-server coverage for prompt/resource progress and input, older protocol servers, and multiple caller identities. Production cache identity, policy/catalog replacement at dispatch, transport cancellation and gateway authorization remain integration obligations.

Documentation conflicts

The Obsidian follow-ups resolve the major changes. Earlier text still describes pre-hook JSON bodies, listing-dependent reads, tasks support and older replay assumptions. Hook overview line 291 and server-config line 50 say no rules run on notifications. The server-config table still describes tasks as exposable. Resource pages sometimes call unlisted fields “empty” while their later text says access must fail. Three progress hook pages are absent.

These are documentation corrections, not reasons to reject the settled decisions. Link superseded sections to their replacement, describe missing unlisted fields as unavailable rather than empty, and document the progress hooks and hard version/task gates. The telemetry safety claim in F07 is the substantive design contradiction.

Elicitation and remaining scope

The atomic-take approach is sound for the promised single-instance stand-in. V3-C02 sends 32 concurrent answers to a genuinely issued challenge under an opaque principal: exactly one forwards and 31 refuse. Existing tests also cover decline/cancel handling, configured-store use, cross-principal refusal and multiple rounds. I am not reopening KV deployment or multi-replica storage choices.

The single-use guarantee applies to stored gateway challenges. Wrapped upstream continuation state remains signed and stateless by explicit decision. It should not be described as a general exactly-once guarantee for upstream side effects. Keep that distinction in the KV/integration handover, and test mixed rounds against the production store and upstream behavior.

I did not perform a new soak, GC tuning exercise, production integration, deployment, exhaustive protocol validation, or review of deferred tasks, MCP Apps and skills implementation. The six-hour soak predates this round. Known retained progress hits and the in-memory store's operational limits remain as recorded in the handover. No claim of bounded memory for arbitrary-duration streams follows from the tests run here.

