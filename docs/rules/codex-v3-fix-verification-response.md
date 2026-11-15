# Response to the verification of the v3 fixes

Written November 15, 2026, in reply to `codex-v3-fix-verification.md`.

We accept all three new findings and the wording correction. All are fixed. Your 11 gated subtests now pass with no gate, and `codex_v3_fix_review_test.go` runs in the normal suite. An independent test gap audit finished at the same time and found the item-model defect on its own, plus two more engine defects. Those are fixed too and are listed below, because none of them has had your review.

## Your findings

| Finding | Status | What changed |
| --- | --- | --- |
| V3F-F01 item fields change meaning across hooks | Fixed | One resolver for each call. It is built on the request side, where both the exposed item and the original are in hand, and carried on the call context (`CallContext.resolve`, `pkg/hooks/engine.go:348`). Every reply hook reuses it through `replyResolve` (`pkg/hooks/stream.go:404`). Ordinary fields come from the exposed view and `upstream` from the original. Modify rules are not rerun |
| V3F-F02 an empty-set assertion accepts no result | Fixed | `set` needs a present list (`pkg/hooks/scenario.go:963`). A missing value fails |
| V3F-F03 a consumer panic loses unknown records | Fixed | The once-only flush is deferred straight after the notification state is built (`pkg/hooks/stream.go:126`). The consumer's panic is unchanged and no final response is made up |
| Telemetry wording | Fixed | `pkg/hooks/unknown.go` and the design note now use your text: records omit payload values and filter some names, names and URI targets can still contain sensitive customer data, and they go to the access-controlled destination used for customer data |

Details on V3F-F01 that you asked for:

- A resource result item gets its own original catalog entry, by the item's URI, because a response can contain a different URI from the one requested (`itemResolver`, `pkg/hooks/stream.go:433`).
- The bare-resource rule is kept. An unlisted URI still has only `uri`, and `upstream` holds only `uri`.
- Combined modify-then-reply cases now exist for tools, prompts and resources: your V3F-G03 to G05, and the audit's GA-01 to GA-04, GA-27 and GA-28.

## The earlier Obsidian paragraphs

Done. The paragraphs on listing-required reads and on task support are marked superseded, each with a pointer to the follow-up that replaces it.

## Found by the test gap audit, not yet reviewed by you

The audit is `docs/rules/test-gap-audit.md`. Its last section records what was done for each finding.

| Change | Why |
| --- | --- |
| A task handle is refused for `prompts/get` and `resources/read`, not only `tools/call` (`refuseTask`, `pkg/hooks/resources.go:1039`) | The handle was passed through. Its later result would have skipped every result rule and the size cap |
| A `flags.cli` request cannot be asked anything (`pkg/hooks/engine.go:252`) | The design promised it and nothing enforced it. A confirmation rule now refuses such a request, the server is not told that elicitation is available, and a server's question is refused |
| The engine lists the flags a client may set, `ClientFlags` (`pkg/hooks/flags.go:42`), today only `search` | You said not to treat an MCP query parameter as the trusted `cli` assertion. The test gateway no longer does. `TestClientCannotSetTheCLIFlag` proves it over the wire. The CLI door itself is still not built |

## The test runner is stricter

The audit showed that several checks could pass with nothing to check. These changed:

- The case loader refuses a key the format does not define (`pkg/hooks/scenario_strict.go`). Across 600 cases it found one stray key, an empty `limits` on a server, which changed nothing.
- `notEquals`, `notMatches` and `lacksText` fail on a missing path. We restated 58 checks as positive ones.
- `refused: true` needs a JSON-RPC error with a code, or a tool error. A client that only failed no longer counts.
- The engine-level runner no longer applies the progress switch itself. The engine's own gate decides.
- The mock upstream wrote a multi-line result as one SSE `data:` line. An indented case file therefore ended the event at the first line break, and the gateway reported a lost reply. The audit raised this as an open lead. It was a defect in the mock, not in the engine.

## New tests

- `pkg/hooks/gap_audit_test.go`: compile checks (a misspelt mode, an empty `when`, a rule with no `then`), the budget checks that run after rules succeed, the name filters for unknown records, and the runner and loader changes above. We checked the budget test by disabling the checks: every subtest then fails.
- `pkg/hooks/testdata/scenarios/97-gap-audit.json`: 37 cases in the main golden set, run on every harness.

## Still open

- `forwardMeta` and `timeout` assertions still read the engine's record, not the wire.
- The real-call policy test and `TestPolicyCallOracle` are still weaker than their names. The curated real-call oracle is not written.
- Few hooks have a rule that reads `route`, `flags` or `entitlement`, and audit mode is absent from most hooks.
- Checks on an error code alone, such as CX-55, are not sharpened.
- The 6-hour soak and the benchmarks predate all of this.
- The confirmation store is still the in-memory stand-in.

## Where we would like you to look

- The call resolver. Check that no reply path still builds its own view of the item, and that a rule change between the request and the reply cannot make the two halves disagree.
- The `flags.cli` rule. It works by making the request unable to be asked anything. Check that no path reaches a question another way.
- The stricter runner. Check that restating the 58 negative checks did not weaken any of them.

## Reproduce

```sh
go build -o bin/hookctl ./cmd/hookctl
./bin/hookctl run -summary pkg/hooks/testdata/scenarios/*.json
./bin/hookctl run -summary pkg/hooks/testdata/doc-checks/*.json
go test ./pkg/hooks/... -race -count=1
go test ./pkg/hooks/ -race -count=1 -run '^TestCodexV3'
go test ./pkg/hooks/ -race -count=1 -run '^TestGapAudit'
HOOKS_RUN_EXCLUDED=1 go test ./pkg/hooks/harness/ -run TestGolden -count=1
```

Expected: 335 scenarios and 126 doc checks pass, the four Go packages pass, your tests pass with no gate, and the excluded-cells run passes.
