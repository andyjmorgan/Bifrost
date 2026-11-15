# Review handover: rules engine, round 3

> Out of date since November 15, 2026, for how the tests are laid out. The matrix package (`pkg/hooks/harness/matrix/`) and the parity bridge (`pkg/hooks/harness/parity_test.go`) no longer exist. Every client-facing case is JSON under `pkg/hooks/testdata/`, run by `TestGolden` in `pkg/hooks/harness/golden_test.go`. See `pkg/hooks/testdata/README.md`, `codex-v3-review-response.md` and `test-gap-audit.md`. The engine decisions described here still stand.


You reviewed this engine twice, at v1 and v2. Since your v2 review it has changed a lot in one day: about 1,500 lines of engine code, new entry points, and one reversal of a rule you argued for. Claude wrote the changes and the tests, and copies of Claude built the test rig, so the work has had no independent review. That is your job here. Treat nothing as proven because a test passes.

Start in `/home/localuser/source/bifrost`, branch `rules-hooks`. Your v2 review was against `ed82ea4`. The work to review is everything after it:

```sh
git log --oneline ed82ea4..HEAD
git diff ed82ea4..HEAD --stat -- pkg/hooks ':!pkg/hooks/testdata' ':!pkg/hooks/harness'
```

## Sources of truth

1. The design note: `/home/localuser/vaults/Datadog/AI Platform/AI Gateway/MCP Gateway - Policy Engine Intent.md`. Read "# v3: Hooks" and every "Follow-up" section at the end. The follow-ups written on November 15, 2026 record the decisions behind this round: streamed replies, servers on older protocol versions, reads are allowed unless denied, unknown things are logged by shape, tasks, MCP Apps.
2. The protocol: MCP 2026-07-28. The front door speaks only that version. Upstream servers may speak any version.
3. The code. Where the code and the design note disagree, that is a finding.

## What changed since your v2 review

| Area | Files | What it does now |
| --- | --- | --- |
| Panic safety | `fault.go`, `state.go` (`parallel`), `eval.go` (`run`) | Every entry point recovers a panic, refuses that request and returns a `Fault` with the rule text and the call. Worker goroutines recover and re-raise on the caller's goroutine. `Config`, `Modify` and `Connect` fail closed when called directly. `Compile` recovers and reports a problem |
| Reply streams | `stream.go`, `hooks.go` | `ToolReplyStream`, `PromptReplyStream` and `ReadReplyStream` take the upstream's events as a Go iterator and yield the client's. New hooks `tools/progress`, `prompts/progress` and `resources/progress` drop or reshape progress. Other notifications are dropped. A lost reply ends with error `-32014` |
| Confirmations | `engine.go` (`continuation`, `elicitation`, `principal`), `statestore.go` | Only an accept with `confirm: true` continues; anything else is `Declined.` State is an opaque random key in a `StateStore`, taken once. The default store is in memory. State is bound to `Request.Principal`, an opaque string from the gateway, falling back to `caller.userId`; with neither, the engine refuses to ask. The signed envelope remains only for wrapping an upstream's own state. A read let through by a confirmation is returned with `ttlMs: 0` |
| Version gate | `engine.go` (`current`), `server.go` (`Config`, `Connect`, `inputAllowed`, `complete`) | A server not on 2026-07-28 gets no elicitation and no progress. Tasks are off for every server and a task handle is refused. Elicitation is declared upstream only when the request's client declared it. A server's input request reaches only such a client |
| Reads | `visibility.go` (`checkURI`, `claims`), `resources.go` (`route`, `fallback`), `items.go` (`bare`), `uris.go` | A read is allowed unless denied, including a URI the server never listed. Such a URI exposes only `uri` to rules; reading any other field fails the rule, which refuses. URIs are never decoded. On the mux, a bare URI routes only to a server that claims it |
| Result type and codes | `fault.go` (`stampResult`), `engine.go` | Every result carries `resultType`, stamped on the way out of each entry point. An unknown tool is `-32602` |
| Unknown things | `unknown.go` | Unknown content blocks, notifications, result types, input requests and template forms are passed or dropped as before, and recorded by shape in `Outcome.Unknown` and through `OnUnknown` |

## Decisions that are settled

The owner made these. Do not argue them; attack how they are implemented.

- Deny is final. Fail closed. The pod must never crash.
- The gateway always answers with a stream. Pre hooks can stop a request; post hooks can only reshape.
- Reads are allowed unless denied. Not listing something does not hide it.
- The gateway never decodes or translates a URI.
- Wrongly typed and null arguments are the customer's concern. The engine does not coerce.
- Client limits are the client's concern. The Python SDK's 1 MiB event limit is recorded, not worked around.
- Extensions come after the core: tasks, skills and MCP Apps are deferred. Skills code exists and is out of scope.
- A rule changes only the field it names.

## Where to attack, in order

1. Reads are allowed unless denied. This reverses your F05, F06, F07, F16 and F17 fixes, on purpose. Look for a read that should be denied and is not:
   - a denied template now covers exactly the URIs it can produce; a template form the gateway cannot parse covers its literal prefix
   - the `bare` item: a rule that reads `name` or `mimeType` on an unlisted URI must fail and refuse. Check short-circuit cases, `upstream.*`, the `resources/read` and `resources/result` hooks, and directory children
   - fallback on the mux uses `claims`, not permission. Check a bare URI that two servers claim, one that none claims, and the new handling of a URI whose own scheme contains `+`
   - content returned under a different URI, and directory children, now pass unless denied
2. Reply streams. `replyStream` separates a panic in the gateway's loop body from one in the engine with an `inYield` flag. Check early exit, a panic at each point, events after the final response, a final refused after progress was relayed, and what a flood of events does to hits and memory. The progress allowance is separate from the request budget; check that one cannot starve the other.
3. Confirmations. Clients cache reads for `ttlMs`, so a confirmed read is now returned with `ttlMs: 0`; look for another path where a cached reply lets a client skip a rule. Check that a challenge really is redeemed once under concurrency, that a key cannot be guessed or moved between callers, methods, tools or policies, that the in-memory store cannot grow without bound, and that nothing still accepts the old signed form for a gateway confirmation. Check rounds that mix a gateway confirmation with an upstream's own question.
4. Panic recovery. `recoveredDirect` decides "called directly or nested" by whether the context carries the request budget; check that this cannot be fooled. `Compile` returns a nil set and a problem on panic; a nil set means no rules, so check that every caller treats a problem as a broken entitlement. Check any exported method on `Set` that a gateway could call without going through an entry point.
5. Version gate. Check where the version comes from, what an empty or odd value does, and that no rule can turn elicitation, progress or tasks back on. The scenario runner defaults a missing version to 2026-07-28; the engine treats it as old. Make sure no test relies on the default to hide a gap.
6. Result stamping. `stampResult` wraps some results in `typedResult`, which changes their Go type. Check for a path that returns a result without `resultType`, and for code that type-asserts a result after stamping.
7. Unknown records. The claim is "names and types, never values". Field names come from the server, so a map keyed by user data would put that data in a shape. Decide whether that breaks the claim.

## Known weak spots

Read these first. They are what the authors would attack.

- The 6-hour soak and the benchmarks predate this round. The read path now builds the resource view more often.
- The confirmation store is an in-memory stand-in. It is wrong for more than one instance.
- Clients cache listings and reads for `ttlMs`, 300 seconds by default. A new deny does not reach content a client already holds until that runs out. Only confirmed reads are exempt.
- `TestCodexV2InterleavedAuditCannotExpireEnforcement` failed once under heavy machine load and passes alone. It gives an enforce rule a 100 ms budget, which a loaded machine under the race detector can exceed.
- About 35 test steps assert only an error code that many places return.
- The three progress hooks have no user doc pages.
- Hits from progress rules accumulate for the life of a stream.
- A `Fault` carries the call's arguments, cut at 64 KB. They may hold customer data.
- The production gateway does not exist. Streaming transport, token mapping and keep-alives are only in the test gateway.

## The test rig

Everything is Go. Python and TypeScript appear only as the SDK clients and servers under test.

| Suite | Where | Run |
| --- | --- | --- |
| Engine Go tests | `pkg/hooks/*_test.go` | `go test ./pkg/hooks/ -race -count=1` |
| Scenarios | `pkg/hooks/testdata/scenarios/` | `./bin/hookctl run -summary pkg/hooks/testdata/scenarios/*.json` |
| Doc checks | `pkg/hooks/testdata/doc-checks/` | `./bin/hookctl run -summary pkg/hooks/testdata/doc-checks/*.json` |
| Harness and parity bridge | `pkg/hooks/harness/` | `go test ./pkg/hooks/harness/ -race -count=1` |
| Client adherence | `pkg/hooks/harness/clients/` | `go test ./pkg/hooks/harness/clients/ -count=1 -v` |
| Reference servers | `pkg/hooks/harness/servers/` | `go test ./pkg/hooks/harness/servers/... -race -count=1` |
| Client by server matrix | `pkg/hooks/harness/matrix/` | `go test ./pkg/hooks/harness/matrix/ -race -count=1 -v` |

Build the runner with `go build -o bin/hookctl ./cmd/hookctl`. Install the SDKs once with `pkg/hooks/harness/clients/setup.sh` and `pkg/hooks/harness/servers/setup.sh`; they install pinned versions from PyPI and npm. A client or server whose runtime is missing is skipped by name. Set `REFSERVERS_REQUIRE=all` to make that a failure. Tests gated behind `HOOKS_KNOWN_GAPS=1` assert behaviour that is decided but not built, or record a client difference.

Results when this was written, all under the race detector: 268 scenarios and 126 doc checks pass. The parity bridge runs 363 of the 393 scenarios and doc checks end to end with the raw client, all matching the engine; the other 30 are engine-only, 28 of them skills. The matrix has 73 cases on 12 client and server pairs: 831 of 876 cells pass, and 45 are cases only the raw client can express.

The matrix (`pkg/hooks/harness/matrix/`) puts each real reference server behind the test gateway and drives it with each client. It has not covered: the progress and input hooks for prompts and reads, because no reference server prompt or resource streams progress or asks a question; real servers on older protocol versions; and more than one caller identity. Those are good places to look.

The parity bridge (`pkg/hooks/harness/parity_test.go`) replays every scenario and doc check end to end, once per client: raw HTTP, the Go SDK, the Python SDK and the TypeScript SDK. Check that it proves what it claims. In particular, look at how it maps `outcome.forward.*` to what the mock server received, and at the expectations it counts but does not evaluate when an SDK folds a confirmation round trip into one call.

## Client findings already recorded

These are differences in the SDK clients, not gateway faults. Do not report them again unless you find the gateway at fault.

- The Python SDK refuses a server-sent event over 1 MiB.
- All three SDKs reject a whole result that has a content block they do not know.
- Python and TypeScript reject some tool results and server questions that are not well formed.
- `@modelcontextprotocol/sdk` 1.x cannot speak 2026-07-28. The 2.x client can, but only with `versionNegotiation` set.

## Rules

- Do not change engine code, existing scenarios or docs. Do not commit or push.
- Prove each finding with a failing test. Put Go tests in `pkg/hooks/codex_v3_review_test.go` and scenarios in `pkg/hooks/testdata/known-gaps/96-codex-v3.json`, so the main suite stays green.
- Do not print or commit credentials. Do not read `authorized_tokens.json` at the repository root. The real fixtures in `internal/e2e/testdata/real/` stay gitignored. Use the network only for the two setup scripts.
- Performance is in scope only where this round made it worse or where it affects correctness.

## Report

Write `docs/rules/codex-v3-review.md`. Use plain, front-loaded writing, with no bold for emphasis. It should have:

- a verdict in one paragraph: ready to wire into Bifrost or not, and why
- findings ranked by severity, each with the file and line, a concrete failure and the test that proves it
- claims you checked and found true, kept short
- tests that prove less than they claim
- what you did not review
