# Response to Codex's third review

Written November 15, 2026, in reply to `codex-v3-review.md`.

We accept all nine findings. None is disputed, and none was a matter of design: each was a defect in how the round 3 changes were built. All nine are fixed, each fix is proven by your own test, and your tests and scenarios now run in the normal suite. The fixes are in commit `ef55dcc`.

Your verdict was "not ready to wire into Bifrost". With these fixes, the reasons you gave for it are gone. We would like you to check that the fixes address the cause and not only your reproducing test, and then look at what has changed since your review, which is listed at the end.

## Findings

| Finding | Status | What changed |
| --- | --- | --- |
| V3-F01 unlisted resources acquire invented metadata | Fixed | A resource the server does not list exposes only `uri`, in every hook. `upstream` on such a resource holds only `uri` and is no longer filled with empty defaults. The input and progress hooks now carry the same "unlisted" mark as the read hook, through `CallContext.Bare` |
| V3-F02 denied template with an empty literal prefix denies nothing | Fixed | Denial and routing are now separate predicates. `denies` covers every URI when a template the gateway cannot parse has no literal prefix. `mayCover`, used for routing claims, still claims nothing in that case |
| V3-F03 native scheme resolution discards ambiguity | Fixed | `route` keeps the fallback's ambiguous and unavailable outcomes, with the audit event and candidates. Only "no server claims it" falls through to reading the first part as a slug |
| V3-F04 a cleanup panic can produce two final responses | Fixed | The stream records that the final response has been emitted before it calls the consumer. A panic after that is reported as a fault and not sent |
| V3-F05 rewritten progress bypasses the size cap | Fixed | The serialized event is checked after rewriting. An oversized event is dropped and counted; the final response keeps its own allowance |
| V3-F06 the public `Set.ListTools` lets a panic escape | Fixed | The public method recovers, lists nothing and reports an issue. The engine calls the unexported `listTools`, which still propagates to the entry point so the fault record is complete |
| V3-F07 shape records are not free of customer data | Fixed, with the claim corrected | See below |
| V3-F08 progress-rule panics disappear from fault reporting | Fixed | The first panic in a progress rule on a stream is reported when it happens, with the rule and the call, and attached to the final outcome. Later ones on the same stream stay as hits, so a stream cannot flood the log |
| V3-F09 lost streams discard unknown records | Fixed | Every ending flushes the records once: a normal response, a lost reply, a refusal after a panic, and the client leaving early. When there is no final outcome to carry them, the callback still receives them |

### V3-F07 in detail

You were right that the safety claim was false. We changed both the implementation and the claim.

- A field name is shown only when it looks like a field name: at most 32 characters, starting with a letter or underscore, made of letters, digits, underscores and hyphens, with at most three digits. Any other field is withheld and counted under `(other fields)` with its type.
- A type or method name that does not look like one, or is longer than 64 characters, is replaced by its length.
- A URI target loses its query and fragment and is cut at 256 characters.
- A template form is reduced to its operator and modifiers, such as `{/…*}`, without variable names.

The design note no longer says the records are safe to log freely. It says they hold no values, that names are filtered because a server can use data as a name, and that what is left is low-sensitivity text a server chose, not public data. An identifier that happens to look like a field name still passes the filter. We think that is the right trade for a record whose purpose is investigation, but it is a judgment, and you may disagree.

## Coverage that proves less than its label

We agree with all four points and have fixed none of them yet.

- The real-call policy test counts categories and does not assert each decision. `TestPolicyCallOracle` uses four synthetic calls and accepts any non-nil result as "refused". Both keep their current names and are known to be weaker than they sound. A small curated set of real calls with exact expected forwards and refusals is the right fix and is not written.
- The scenario fuzzer proves panic resistance for the inputs it runs and nothing about authorization.
- The bare-resource and stream gaps you named are now covered by your own tests, which run in the normal suite.
- `observedForward` still takes `forwardMeta` and `timeout` from the engine's outcome and not from the wire. The runner around it was rewritten since your review, but that function kept this behaviour. An assertion on those two fields does not prove a transport applied them.

## Documentation conflicts

Fixed:

- three new pages for the progress hooks: `tools-progress.md`, `prompts-progress.md` and `resources-progress.md`
- the overview and the `server/config` page no longer say that no rules run on notifications, and no longer describe tasks as something a rule can turn on
- the resource filter page says an unlisted resource's other fields are unavailable, not empty
- the unknown-record safety claim, as above

Not fixed: earlier paragraphs of the design note still describe pre-hook JSON bodies, listing-dependent reads and task support. The follow-up sections override them, but the old text is not yet marked as superseded.

## What has changed since your review

You reviewed `34136a3`, with `0d916d3` arriving under you. These changes came after, and none has had an independent review.

1. Request flags. `route` is now only `direct` or `mux`. Search mode is no longer a third route value: it is a flag, and a request can carry several. Rules read them as fields of one variable, `flags.search` and `flags.cli`. Every known flag is always present. A name that is not a flag, or any use of `flags` other than `flags.<name>`, is refused when the rule is saved (`pkg/hooks/flags.go`, `checkFlags`). The older spelling, a request whose route is `search`, is still accepted and reads as the mux with the search flag on.
2. Hook schemas. `pkg/hooks/schema.go` generates, from the registry, what each hook's rules may read and set. `hookctl hooks` prints it. Two tests tie the doc pages and the schemas to the registry.
3. One golden test set. Every client-facing test case is now JSON, under `pkg/hooks/testdata/`: 564 cases in all. That is 298 scenarios, 126 doc checks, 126 end-to-end cases and 14 raw-client outliers. One runner, `TestGolden` in `pkg/hooks/harness/golden_test.go`, takes each case through raw HTTP and the Go, Python and TypeScript SDK clients, against the mock upstream or the real reference servers. The matrix package and most of the adherence battery you ran no longer exist as Go code; their cases are in `testdata/e2e/`.
4. Explicit exclusions. A case a harness cannot run stays in the set and carries an `exclude` entry naming the harness and the reason. No Go code decides a skip. `HOOKS_RUN_EXCLUDED=1` runs excluded cells and fails on any that pass.

## Where we would like you to look next

- The nine fixes. Check each against its cause. In particular: whether any other path builds a resource item without the unlisted mark; whether `denies` and `mayCover` are each used in the right place; whether any stream ending still skips the flush.
- Flags. Look for a way to read or influence a flag that the compile check misses: comprehensions, macros, `has()`, a variable shadowing `flags`, and the older `search` route spelling. `flags.search` is set by the client, so check that nothing in the engine itself trusts it for a security decision.
- The golden runner. It replaced the bridge you examined. The exclusion reasons were generated from what each harness reported and then sampled, not all hand-checked. The SDK columns no longer fold a hand-written confirmation round trip into one SDK call: those steps are excluded for the SDKs and made by the raw client, with SDK confirmation flows covered by separate cases. Check that this did not lose coverage that mattered.
- The two weak tests named above, if you want to write the curated real-call oracle yourself.

A separate audit of test gaps is in progress and will be written to `docs/rules/test-gap-audit.md`.

## Still open from the handover

- The 6-hour soak and the benchmarks predate both this round and the fixes.
- The confirmation store is an in-memory stand-in and is wrong for more than one instance.
- Clients cache listings and reads for `ttlMs`; a new deny does not reach content a client already holds until that runs out.
- The production gateway does not exist. Streaming transport, token mapping and keep-alives are only in the test gateway.

## Reproduce

```sh
go build -o bin/hookctl ./cmd/hookctl
./bin/hookctl run -summary pkg/hooks/testdata/scenarios/*.json
./bin/hookctl run -summary pkg/hooks/testdata/doc-checks/*.json
go test ./pkg/hooks/... -race -count=1
go test ./pkg/hooks/ -race -count=1 -run '^TestCodexV3'
HOOKS_RUN_EXCLUDED=1 go test ./pkg/hooks/harness/ -run TestGolden -count=1
```

Expected: 298 scenarios and 126 doc checks pass, the Go suites pass, your thirteen round 3 tests (ten findings and three controls) pass with no gate, and the excluded-cells run passes.
