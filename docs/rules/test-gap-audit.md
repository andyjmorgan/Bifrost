# Rules engine test gap audit

November 15, 2026. Branch `rules-hooks` at `ef55dcc`. Independent review of the tests, not the design.

## Verdict

The tests are broad for the tool path on the direct route and thin almost everywhere else. Line coverage of the engine files is high (93% once every source is combined), but coverage hides that most hooks are exercised with one route, one mode and a handful of variables. Writing the missing cases found two real engine bugs and one unenforced design promise. The three gaps that matter most:

1. Reply-side tool rules read the upstream tool, not the exposed one. `tools/result`, `tools/input` and `tools/progress` rules that test `name`, `description` or any other item field see the tool as the upstream sent it, before `tools/modify`. The docs say "the exposed tool". A deny written against a renamed tool silently never fires. No test combined a rename with a reply-side rule on the item. Cases GA-01 to GA-04 fail.
2. A task handle passes through `prompts/get` and `resources/read` replies. Only `tools/call` refuses it. The design says a task handle is refused, because its later result would skip every result rule. Cases GA-13 and GA-14 fail.
3. The dynamic variables and audit mode are untested outside a few tool hooks. No rule in the golden set reads `route` or `flags` in any hook but `tools/result`, and none reads `entitlement` anywhere. Audit mode appears only on four tool hooks. `flags.cli`, which the design says "can be relied on" because the gateway sets it, is accepted from any client's query string by the reference gateway, and the engine still offers confirmation on a `cli` request (GA-22 fails).

## Gaps, ranked by damage

### 1. Reply-side tool rules see the upstream tool (bug)

Not tested: any `tools/result`, `tools/input` or `tools/progress` rule that reads an item field of a tool that `tools/modify` changed.

Code: `pkg/hooks/server.go:369` builds the reply-side item as `prepare(upstreamTool(s, tr.Upstream))` and resolves `name`, `description` and the rest from it (`server.go:379`). The same resolver feeds `tools/input` (`server.go:414`). `pkg/hooks/stream.go:409` does the same for `tools/progress`. The docs (`docs/rules/hooks/tools-result.md` line 20, `tools-input.md` line 21, `tools-progress.md` line 23) say these fields are the exposed tool. Prompts and resources get this right: GA-27 and GA-28 pass.

Bug that slips through today: an administrator renames `echo` to `say` and writes `tools/result: name == 'say' -> decision: deny` to withhold its output. The rule evaluates false and leaves no hit. Results flow unredacted.

Evidence: GA-01 (rename, `tools/result` on `name`), GA-02 (`description`), GA-03 (`tools/progress`), GA-04 (`tools/input`). All four fail on the engine and on every client over the wire.

Fix the test set: keep GA-01 to GA-04, and add the same pairing to the `tools/result` doc-checks.

### 2. Task handles leak through prompts and reads (bug)

Not tested: a `resultType: "task"` reply to anything but `tools/call`.

Code: `pkg/hooks/resources.go:206` (`UpstreamPromptReply`) and `resources.go:907` (`UpstreamReadReply`) record the unknown result type as "passed" and return the reply. `server.go:393-406` refuses it for tools only. The design's unknown-things table says "a task handle is refused", and the tasks follow-up says the engine "refuses a task handle a server returns unasked".

Bug that slips through today: a server answers `resources/read` with a task handle. The client receives it, polls `tasks/get` elsewhere, and the eventual content never passes `resources/result` rules or the size cap. For a read, the engine also adds `"contents": []` and cache hints to the task reply, so the client gets a malformed hybrid.

Evidence: GA-13 and GA-14 fail on the engine and on raw, Go and Python clients. The TypeScript SDK happens to reject the reply itself, so that cell passes.

### 3. `flags.cli` is client-settable and does not stop confirmation

Not tested: that a client cannot set `flags.cli`, and that a confirmation rule refuses a `cli` request.

Code: `pkg/hooks/harness/gateway.go:445` sets every known flag, `cli` included, from the query string of any MCP request, outside the test hooks, with the comment "as a real gateway would". The engine reads `FlagCLI` nowhere (`grep -n FlagCLI pkg/hooks/*.go` finds only the constant and its schema text). The design says `flags.cli` "is set by the gateway itself, so it can be relied on" and that a confirmation rule "refuses such a call".

Bug that slips through today: a rule relaxes a deny for `flags.cli` (trusted, per the design). Any MCP client appends `?cli=true` and gets the relaxed behaviour. Separately, a `cli` request whose client map declares elicitation is offered a confirmation it cannot answer.

Evidence: GA-22 fails on the engine and on raw. The flag-reading part is a gateway defect modelled by the harness, not an engine bug; whether the engine should enforce no-confirmation on `cli` is a design decision to make explicit. Only 3 cases set `cli` at all, all in `95-fable-review.json`, all on `tools/result`.

### 4. Dynamic variables and modes are an almost empty grid

Built from every rule in the golden set (script over all case files) plus a grep of the Go tests:

| Variable or mode | Hooks where any rule uses it |
| --- | --- |
| `route` | `tools/result` only (golden and `fable_review_test.go:1183`) |
| `flags.search`, `flags.cli` read | `tools/result` only |
| `entitlement` | none (`grep -rn 'entitlement\.' pkg/hooks/testdata pkg/hooks/*_test.go` is empty) |
| audit mode | `tools/call` (3), `tools/filter` (3), `tools/modify` (1), `tools/result` (1), `tools/progress` (Go only) |
| `mux` route | zero cases for `prompts/filter`, `prompts/input`, `prompts/progress`, `resources/input`, `resources/modify`, `resources/progress`, `tools/input`, `skills/filter` |
| `prompts/progress`, `resources/progress` | one case each, `message` only; deny never tested |

Why it matters: rules reading `route` or `flags` in a filter or modify hook change listings per request, and the planner's dynamic checks are hand-written per variable. One already disagrees: `planSkill` (`resources.go:395`) skips rules that read `caller`, `client` or `now`, but not `flags` or `route`, though both are in `DynamicVars`. Skills are deferred, so this is low severity today, but the pattern will recur.

New cases, all passing: GA-20 (`route` in `tools/filter` on the mux), GA-21 (`flags.search` in `tools/filter`, through `tool_search`, `get_tool_definition` and `execute_tool`), GA-23 (a pin that reads a flag is refused at save), GA-24 (`entitlement`), GA-05 to GA-08 (audit mode: a `then` that errors, `resources/filter`, `server/config`, `prompts/get` elicit), GA-15 (progress deny on prompts and reads), GA-25 and GA-34 (resources/modify and prompts on the mux).

### 5. Elicitation continuation edge cases

Not tested before this audit, now covered and passing:

- An upstream answer for a key the upstream never asked (`engine.go:792`). Without it a client could inject extra `inputResponses` into an upstream round. GA-09.
- A server asking for input on a request with no caller identity (`server.go:625`). GA-10.
- A confirmation rule with no `has(input.x)` guard (`engine.go:843`). GA-33.

### 6. Limits on non-tool replies

Not tested: the size cap on `prompts/get` replies (`resources.go:195`) and read replies (`resources.go:896`), and the drop of an oversized progress event (`stream.go:285`). A regression would let a hostile server push unbounded content through prompts or reads. GA-11, GA-12 and GA-16 pass.

Still untested: the budget checks that run after rules succeed and before forwarding (`engine.go:530`, `engine.go:570`, `resources.go:164`, `resources.go:854`, `resources.go:879`). If removed, a request whose rules used up the budget but did not fail would still be forwarded. A case needs a rule that finishes just past a tiny budget, which the JSON format cannot pin down reliably; this wants a Go test with an injected clock.

### 7. `_meta` handling outside tools

Not tested: the `_meta` allowlist on prompts, resources and templates (`engine.go:914-928` is reached only by the wire harness, and no expectation checks it; `metaAllow` appears only on tool catalogs), reading `meta` on a prompt or resource (`items.go:51-55`), deleting a key with `meta.x: null` on a non-tool item (`items.go:102`), and `server/connect` `meta` on prompts and reads (`resources.go:169`, `resources.go:876`). The MCP Apps decision depends on the allowlist stripping `ui` everywhere. GA-29 to GA-32 pass.

### 8. Template and identity edge cases

Passing new cases: a template with unbalanced braces is unsupported and its deny covers its literal prefix (GA-26, `visibility.go:56`); renaming a prompt or tool to the empty string hides it (GA-17, GA-18; `items.go:165`, `tools.go:582`); adding a parameter whose name exists hides the tool (GA-19, `tools.go:442`).

## Coverage

`go test ./pkg/hooks/ -coverprofile` gives 87.3% of statements. The JSON cases do not all reach that profile: `hooks_test.go` loads only `testdata/scenarios`. With a scratch test that ran every golden case through `Scenario.Run` (deleted afterwards), and `TestGolden` with `-coverpkg=./pkg/hooks`:

| Source | All files | Engine files only (without `scenario.go`, `schema.go`, `profile.go`) |
| --- | --- | --- |
| Go unit tests | 87.3% | 91.3% |
| Golden set, engine runner | 83.1% | 86.7% |
| Golden set, over the wire | 84.9% | 87.6% |
| All three | 91.4% | 92.4% |
| All three plus `97-gap-audit.json` | 92.2% | 93.1% |

Uncovered code that still matters after the new cases:

- The post-rule budget checks listed in gap 6.
- `rule.go:214-241`: an invalid `mode`, an empty `when` and a filter rule with no `then` are never compiled in a test. A typo such as `"mode": "audt"` is refused today; nothing would notice if that check went, and the rule would then enforce.
- `resources.go:996`: a read result whose result rule errors returns a generic error. Untested; a regression could forward the unchecked content.
- `resources.go:414-418`: a skill rewrite that a result rule denies. Deferred extension.
- `unknown.go:94-180`: most of the name filter and type families for unknown records. These guard what reaches logs; `fieldName` is 60% covered.

Uncovered code that does not matter: `eval.go:374-408`, `(*budget).start`, is dead (the only caller of `.start` is `runBudget` at `eval.go:120`); `view.go:71` `Overlay`, `uris.go:281` `matchTemplate` and `rule.go:133` `Rules` have no callers in the engine; nil-parameter guards in `stream.go:75-106`.

## Tests that prove less than they claim

- End-to-end assertions that are not on the wire. `outcome.forward` is built from what the mock received, except `connect.forwardMeta` and `timeout`, which are copied from the engine's own forward (`golden_test.go:1075`, `golden_test.go:1079`). Against the reference servers the whole of `outcome.forward` is the engine's (`golden_test.go:820`). Every other key of `outcome` and `reply` except `result` and `error`, and all of `hits`, `issues`, `fallback`, `fault` and `unknown`, are the gateway's record of the engine outcome. So every `server/connect` `meta` case and every `tools/call` `timeout` case asserts the engine's intention, not the wire. The one exception is the e2e timeout case that checks the slow call is actually cut off.
- `refused: true` passes on a client failure. A failed SDK call becomes `client.error` with code 0 (`golden_test.go:790`), and `refused` holds for any `error` (`scenario.go:986`). A case that only checks `refused` passes when the SDK crashed or timed out.
- Negative operators pass when the path is missing. `notEquals`, `notMatches` and `lacksText` hold on an absent value (`scenario.go:917-985`). 61 of these have no positive check on the same path. Example: `e2e/mock-upstream-view.json` "upstream _meta is stripped ... the ui key included" step 0 checks `tools[0]._meta` absent and `tools` lacks the text; it passes on an error or an empty listing.
- Error-code-only checks. `-32602` is returned by about twenty different refusals. CX-55 ("unknown answer cannot be appended to valid state") checks only `-32602` and no forward, so it passes if the state were rejected for the wrong reason. `e2e/errors.json` "errors crash" checks only `-32603`, which the gateway also uses for "unknown upstream" and "upstream request failed".
- The engine runner applies the progress switch itself. `scenario.go:1226` withholds progress events when `server/config` turns progress off, so engine-level cases for "progress off" never reach the engine's own gate. The wire cases that use `forceProgress` do test it.
- The case loader ignores unknown keys. `LoadScenarios` does not use `DisallowUnknownFields`. While writing GA-26 I used `catalog.templates` (the key is `resourceTemplates`); the case loaded with no templates and tested nothing. Any existing case with such a typo would pass vacuously. Making the loader strict would find them.

## Exclusions that hide a gap

There are 841 exclusion entries, counted per harness named (205 on cases, 636 on steps) with 190 distinct reasons. None excludes a case or step from every harness.

- A step excluded for an SDK is made by the raw client in its place (`golden_test.go:589-613`). Those 636 step entries add raw runs, not SDK coverage.
- 30 cases run on the engine only. 22 are skills (deferred). The rest hide real behaviour from every client: "server/connect is carried in every forward, with the _meta policy applied" (`09-v2.json`), CX-40 "direct search results use names executable on that direct route", CX-43 listing order, RH-FRS-01 `ttlMs` and `cacheScope` on list results, CX-37 skill list size cap, CX-67 Skills extension in discover.
- 185 further steps run on no SDK. Most are hand-written continuations (correctly raw-only). The rest are `discover`, `connect` and `config` results and directory reads, which no SDK exposes. So no SDK test shows what an SDK makes of the discover result shaped by `server/config` and `server/modify`, or of `resources/directory/read`.
- Spot checks of reasons. I confirmed by running cases that three generated reasons are true: the Python SDK sends no progress token on `prompts/get` (GA-27), the Python driver passes no request `_meta` (GA-30), and the TypeScript SDK skips `tools/list` when discovery advertises no tools (GA-23). Small `maxResponseBytes` limits stop every SDK from connecting, because discovery itself exceeds the cap; any future limit case needs a limit above about 4 KB or an SDK exclusion.

## Open lead: lost replies under the full wire run

Over the full `TestGolden` run, GA-15, GA-16 and GA-36 (all with scripted progress) end in `-32014` "reply was lost" on every client, raw included, in three of three runs. Each passes when its file holds only that case. The upstream stream reached EOF before its final message (`gateway.go:875`). I did not find the cause and stopped at the owner's request. It may be harness interference between cases, or a stream defect that only shows under load. It needs a look before these cases join the main set.

## Checked and found well covered

Tool listing and calling on the direct route; parameter rename, hide, pin, default, enum and bounds; argument decoding (duplicate keys, deep nesting, large integers); confirmation binding (caller, entitlement, generation, fingerprint, argument hash, single use); fallback resolution and ambiguity on the mux; unlisted reads and bare-item failure (after the v3 fixes); tool result shaping with `drop`; size caps on tool results and listings; lost replies and unknown notifications on tool streams; panic recovery in public entry points.

## Not audited

Skills internals beyond noting the dynamic-variable mismatch; the reference servers' own tests; the client drivers' fidelity to each SDK; performance and soak results; `profile.go` and `schema.go`; the concurrency of rule changes between a request and its reply (only reachable from Go, and the design decides it only for tasks).

## New cases

`pkg/hooks/testdata/known-gaps/97-gap-audit.json`, 36 cases, run with `./bin/hookctl run -summary pkg/hooks/testdata/known-gaps/97-gap-audit.json`.

| Result on the engine | Cases |
| --- | --- |
| Fail: engine bug | GA-01, GA-02, GA-03, GA-04 (reply-side tool item), GA-13, GA-14 (task handles) |
| Fail: unenforced design promise | GA-22 (`flags.cli` confirmation) |
| Pass: correct but previously untested | GA-05 to GA-12, GA-15 to GA-21, GA-23 to GA-36 |

Over the wire the failing cases fail on every client the same way, except that the TypeScript SDK rejects a task handle itself, so GA-13 and GA-14 pass in its column. GA-15, GA-16 and GA-36 hit the lost-reply lead above in the full run.

## What was done about it

Added November 15, 2026, by the engine's author, after the audit. Uncommitted when written.

| Finding | Status |
| --- | --- |
| Reply-side tool rules read the upstream tool (GA-01 to GA-04) | Fixed. One resolver per call, built on the request side and reused by every reply hook. Also found by Codex as V3F-F01 |
| Task handles pass through prompts and reads (GA-13, GA-14) | Fixed. Refused for a tool call, a prompt and a read |
| `flags.cli` is client-settable and does not stop confirmation (GA-22) | Fixed. A `flags.cli` request cannot be asked anything. The engine lists the flags a client may set (`ClientFlags`); the test gateway takes `cli` only from its own side. `TestClientCannotSetTheCLIFlag` proves it over the wire |
| Lost replies under the full wire run (GA-15, GA-16, GA-36) | Not an engine defect. The mock upstream wrote a multi-line result as one SSE `data:` line, so an indented case file ended the event at the first line break. The mock now writes one `data:` line for each line |
| The case loader ignores unknown keys | Fixed. The loader refuses a key the format does not define. It found one stray key in 600 cases, an empty `limits` on a server, which changed nothing |
| Negative operators pass on a missing path | Fixed. `notEquals`, `notMatches` and `lacksText` need a present value. 58 checks were restated as positive ones |
| `refused: true` passes on a client failure | Fixed. A refusal needs a JSON-RPC error with a code, or a tool error. After Codex's state review, `refused: false` also needs a result, so a client failure passes neither; the three steps it named now assert the result the client received |
| The engine runner applies the progress switch itself | Fixed. The runner models only the gateway's token mapping; the engine's own gate decides |
| Post-rule budget checks, compile checks, name filters untested | Covered in `pkg/hooks/gap_audit_test.go`. The budget test was checked by disabling the checks: every subtest then fails |
| A read-result rule that fails to evaluate (`resources.go:996`) | The rule failure is a deny before that line; covered by GA-37. Line 996 itself stays a defensive branch with no test |
| The 36 cases | Moved to `testdata/scenarios/97-gap-audit.json`, now 37 cases, in the main set with exclusions |

Second pass, later on November 15, 2026:

| Finding | Status |
| --- | --- |
| The thin grid | Covered. `testdata/scenarios/98-grid.json` holds 154 cases: for each of 22 hooks (all but the deferred `skills/filter`), a rule that reads `route`, a flag and the entitlement, each as a pair where it fires and where it does not, and a rule in audit mode. Every hook now runs on the mux, with a second server whose same-named items must stay untouched. All pass on the engine and on every client; no engine defect was found |
| `forwardMeta` is the engine's record, not the wire's | Fixed. On every forward in the golden set, the runner compares the engine's decision with the `_meta` the mock upstream received. Breaking the engine either way (never forward, always forward) fails the tool, prompt and read cases |
| `timeout` is the engine's record | Covered on the mock in `testdata/e2e/mock-timeout.json`: a timeout shorter than the server's delay cuts the call off and the server sees the cancel; a timeout the server beats changes nothing |
| An upstream that fails before replying (found while writing the timeout cases) | Fixed in the test gateway. It sent the client `-32603` with the transport's own error text, which names the upstream's address. It now hands the failure to the engine, so the client gets the one lost-reply answer (`-32014`) and the cause goes to the record |
| Wildcard checks (raised by Codex) | Fixed. A `[*]` path that collects nothing needs the list it collects from to be present, and an empty collection now compares as `[]` |
| Soak | The soak gained two operations: a streamed reply through progress and result rules, and a confirmation that is asked and never answered |

### Soak result

Run November 15 to 16, 2026, 20:51 to 02:51 UTC, on 10 cores, with `HOOKS_SOAK=6h`. The test binary was built from the tree that became commit `94fcf43`; a rebuild at that commit has the same SHA-256 (`94d4e8d7…7d25e`).

| Measure | Result |
| --- | --- |
| Outcome | Passed, 0 failures, leak check passed |
| Operations | 987,483, about 46 a second |
| Mix | 324,045 calls, 246,306 listings, 98,849 streamed replies, 98,661 confirmation round trips, 98,033 fallback reads, 49,255 catalog refreshes, 39,774 searches, 19,840 abandoned confirmations, 9,828 runs of the whole scenario suite |
| Live heap | 82 to 165 MiB. The first quarter averaged 123 MiB and the last quarter 126 MiB, so no growth |
| Goroutines | 42 to 112, no growth |
| Pending confirmations | 240 to 380 after the first 10 minutes. Abandoned ones expire and are dropped |
| Garbage collection | 107 seconds of pause in 6 hours, 0.5% of the run |

This is the engine alone, with no network. It says nothing about the gateway's transport, and it is not a throughput benchmark: one operation in a hundred runs the whole scenario suite.

Not done:

- against the reference servers, `outcome.forward` is still the engine's record: only the mock upstream shows what was received
- error-code-only checks (CX-55, "errors crash") are not sharpened
- the 30 engine-only cases and the steps no SDK can make are unchanged
- `planSkill` does not skip rules that read `flags` or `route` (skills are deferred)
- dead code is not removed
