# Rules engine test cases

Every question raised while designing the hooks model, written as a test case. Each case has an ID that the test that proves it carries in its name or comment, so `grep -r "RH-CALL-03"` finds the proof.

The design is "v3: Hooks" in the "MCP Gateway - Policy Engine Intent" note. The engine is the standalone library `pkg/hooks` with the `hookctl` CLI; it is not wired into the Bifrost gateway yet.

## How the tests are built

Every test is written in Go and runs with the Go toolchain. There are no Python tests.

- Go tests are the `*_test.go` files in `pkg/hooks/` and `pkg/hooks/harness/`. `go test` runs them.
- Scenario files are JSON data, not code. Each one describes servers, rules, requests and expected outcomes. A Go runner executes them: `pkg/hooks/scenario.go`, through the `hookctl` CLI in `cmd/hookctl`, which is also Go.
- The two Python files in `scripts/fixtures/` are not tests. They download the real tool catalogs used as fixtures. No test calls them.

| Suite | Where | What it proves | How to run |
| --- | --- | --- | --- |
| Go tests | `pkg/hooks/*_test.go` | Unit, corpus, fuzz, benchmark and soak tests of the engine, including panic safety, confirmations and reply streams | `go test ./pkg/hooks/ -race -count=1` |
| Scenarios | `pkg/hooks/testdata/scenarios/*.json` | Full requests through the engine with simulated upstream replies. Each scenario lists the IDs it proves | `./bin/hookctl run -summary pkg/hooks/testdata/scenarios/*.json` |
| Doc checks | `pkg/hooks/testdata/doc-checks/*.json` | Every example rule in `docs/rules/hooks/` behaves as its page says | `./bin/hookctl run -summary pkg/hooks/testdata/doc-checks/*.json` |
| Known gaps | `pkg/hooks/testdata/known-gaps/*.json` | Behaviour that is decided but not built. These fail until the engine catches up, and sit outside the main folder so it stays green | `./bin/hookctl run -summary pkg/hooks/testdata/known-gaps/*.json` |
| Harness | `pkg/hooks/harness/` | End to end over real HTTP: the official Go SDK client and a raw HTTP client, through a test gateway built on the engine, to scriptable mock MCP servers | `go test ./pkg/hooks/harness/ -race -count=1` |
| Soak | `TestSoak` in `pkg/hooks/soak_test.go` | Hours of mixed load with no failure, leak or change to the shared catalog | `HOOKS_SOAK=6h go test ./pkg/hooks/ -run TestSoak -timeout 0` |

Run everything that should pass:

```
go build -o bin/hookctl ./cmd/hookctl
./bin/hookctl run -summary pkg/hooks/testdata/scenarios/*.json
./bin/hookctl run -summary pkg/hooks/testdata/doc-checks/*.json
go test ./pkg/hooks/... -race -count=1
```

Some Go tests assert behaviour that is decided but not built. They are skipped unless `HOOKS_KNOWN_GAPS=1` is set, and fail when it is, until the engine catches up.

Levels in the tables below:

- `unit` and `e2e` cases are proven in `pkg/hooks`, by a scenario file that lists the ID or by a Go test that carries the ID in a comment. "e2e" here means a full request through the engine, with simulated upstream replies. Tests over real HTTP are in the harness.
- `integration` cases need the gateway itself: catalog fetching, caching and refresh. They are deferred until the engine is wired into Bifrost.

Real tool catalogs (about 1,500 tools from Datadog, GitHub, public MCP servers and the Bifrost test servers) and 936 real tool-call examples live in `internal/e2e/testdata/real/`, which is gitignored. `scripts/fixtures/` regenerates them. Tests that need them skip when they are absent.

## Hooks and rule shape

| ID | Question | Expected | Level |
| --- | --- | --- | --- |
| RH-HOOK-01 | Can a rule set a field its hook does not have? | No. Saving fails with a problem naming the field | unit |
| RH-HOOK-02 | Can a rule read a variable its hook does not have, such as `args` in `tools/filter`? | No. Saving fails | unit |
| RH-HOOK-03 | Is an unknown hook name accepted? | No. Saving fails | unit |
| RH-HOOK-04 | Can a modify hook decide visibility? | No. `decision` is not a modify field | unit |
| RH-HOOK-05 | Can any hook write `caller` or `client`? | No. Saving fails | unit |
| RH-HOOK-06 | Is `completion/filter` a hook? | No. Completions are not supported | unit |
| RH-HOOK-07 | Does a rule list that does not compile fail the entitlement closed? | Yes. Listings expose nothing for that slug and report the problem | e2e |
| RH-HOOK-08 | Is a disabled rule skipped? | Yes | unit |

## CEL language

| ID | Question | Expected | Level |
| --- | --- | --- | --- |
| RH-CEL-01 | Can `when` use `&&`, `||`, `!` and parentheses? | Yes, with CEL precedence | unit |
| RH-CEL-02 | Can `when` use a ternary? | Yes | unit |
| RH-CEL-03 | Can a `then` value use a ternary? | Yes | unit |
| RH-CEL-04 | Can rules prepend and append text with `+`? | Yes | unit |
| RH-CEL-05 | Is `regex.replace` available? | Yes, from `ext.Regex` | unit |
| RH-CEL-06 | Is `"%s".format()` available? | Yes, from `ext.Strings` | unit |
| RH-CEL-07 | Does `has()` guard an optional field? | Yes | unit |
| RH-CEL-08 | Does `false && <error>` evaluate to false without an error? | Yes | unit |
| RH-CEL-09 | Are list extensions (`slice`), two-variable comprehensions (`transformMap`) and sets available? | Yes | unit |
| RH-CEL-10 | Is the old `regexReplace` function gone? | Yes. Using it fails to compile | unit |
| RH-CEL-11 | Does an expression that is not boolean in `when` fail to compile? | Yes | unit |

## Evaluation and decisions

| ID | Question | Expected | Level |
| --- | --- | --- | --- |
| RH-EVAL-01 | With no matching rule, is the item visible and callable? | Yes | unit |
| RH-EVAL-02 | Is a deny final? Can a later rule re-allow? | A deny is final. There is no allow decision | unit |
| RH-EVAL-03 | Org rule denies `delete_*`, entitlement rule tries to re-allow admins. What does an admin see? | The tool stays hidden | unit |
| RH-EVAL-04 | Does the first deny stop evaluation for that item? | Yes. Later enforce rules do not run | unit |
| RH-EVAL-05 | Does an enforce rule that fails to evaluate deny? | Yes, and it stops | unit |
| RH-EVAL-06 | Does an audit rule that matches change the outcome? | No. It is logged as "would deny" | unit |
| RH-EVAL-07 | Does an audit rule that fails change the outcome? | No | unit |
| RH-EVAL-08 | Do audit rules still run after a stop? | Yes, for a complete log | unit |
| RH-EVAL-09 | In a call hook, do patches chain? | Yes. A later rule sees an earlier patch | unit |
| RH-EVAL-10 | In a call hook, does a deny after an elicit run? | No. `elicit()` stops evaluation | unit |
| RH-EVAL-11 | Does each item run through the list on its own? | Yes. One item's deny does not affect another item | unit |

## Item model

| ID | Question | Expected | Level |
| --- | --- | --- | --- |
| RH-ITEM-01 | Are item fields top-level variables? | Yes: `name`, `description` and so on | unit |
| RH-ITEM-02 | Does `upstream.name` stay the same after a rename? | Yes | unit |
| RH-ITEM-03 | Does a rule after a rename see the new `name`? | Yes. Changes chain | unit |
| RH-ITEM-04 | Does a `_meta` key a rule adds reach the client? | Yes, unless a later rule removes it | e2e |
| RH-ITEM-05 | Is `upstream` writable? | No. Saving fails | unit |

## Copy-on-write

| ID | Question | Expected | Level |
| --- | --- | --- | --- |
| RH-COW-01 | Is an item with no changes shared with the cached catalog? | Yes. Its overlay is empty and its output is the upstream object | unit |
| RH-COW-02 | Does changing one parameter description copy only that path? | Yes. Other parameters still point at the upstream object | unit |
| RH-COW-03 | Does the built JSON equal the upstream plus the overlay? | Yes | unit |
| RH-COW-04 | Does building the output change the cached upstream object? | No | unit |
| RH-COW-05 | Do 1,000 tools with 20 changed produce 20 overlays? | Yes | unit |

## Reversal

| ID | Question | Expected | Level |
| --- | --- | --- | --- |
| RH-REV-01 | Is a renamed tool called upstream by its original name? | Yes | e2e |
| RH-REV-02 | Do chained renames resolve to the upstream name? | Yes | unit |
| RH-REV-03 | Upstream `a` renamed to `b`, upstream `b` hidden. The client sends `b`. What reaches the upstream? | `a` with the client's value. Upstream `b` gets only gateway values | e2e |
| RH-REV-04 | Does a chain of parameter renames `cql`, `query`, `q` map `q` back to `cql`? | Yes | unit |
| RH-REV-05 | Is a client value for a hidden parameter rejected? | Yes, as an invalid argument | e2e |
| RH-REV-06 | Can a required upstream parameter be hidden without `pin` or `default`? | No. Saving or evaluation fails | unit |
| RH-REV-07 | Does a pinned value reach the upstream, and is a different client value rejected? | Yes, both | e2e |
| RH-REV-08 | Is a default filled only when the argument is missing? | Yes | e2e |
| RH-REV-09 | Are `enum`, `minimum` and `maximum` enforced on call? | Yes | e2e |
| RH-REV-10 | Is an added parameter removed before forwarding? | Yes | e2e |
| RH-REV-11 | Do two tools renamed to the same name fail closed? | Yes. Both are hidden and the listing reports it | unit |
| RH-REV-12 | Do two parameters renamed to the same key fail closed? | Yes | unit |
| RH-REV-13 | Upstream `a` is renamed onto the name of hidden upstream `b`. Is that a collision? | No. The client's `b` means `a`, and upstream `b` gets only gateway values. Proven by RH-REV-03 | e2e |
| RH-REV-14 | Is a pin static, so `caller.email` cannot be pinned in modify? | Yes. Caller values go in `tools/call` as `args.<p>` | unit |
| RH-REV-15 | Does a pin to the caller work as two rules, hide plus `args.<p>: caller.email`? | Yes | e2e |
| RH-REV-16 | Does the old exposed name fail after a rename? | Yes, with unknown tool | e2e |
| RH-REV-17 | Do renamed prompt arguments map back on `prompts/get`? | Yes | e2e |

## Tool calls

| ID | Question | Expected | Level |
| --- | --- | --- | --- |
| RH-CALL-01 | Default `space` is `HR` and a call rule denies `HR`. The client omits `space`. Allowed? | Denied. Rules see effective arguments | e2e |
| RH-CALL-02 | Does a clamp patch reach the upstream? | Yes | e2e |
| RH-CALL-03 | Is a patch that moves a value out of range rejected? | Yes, by the check after the rules | unit |
| RH-CALL-04 | Is a deny reason returned as `isError` text? | Yes | e2e |
| RH-CALL-05 | Does a time-range rule with `timestamp` and `duration` work? | Yes | unit |
| RH-CALL-06 | Is a tool hidden by a filter refused when called directly? | Yes, with -32004, the same as a tool that does not exist | e2e |
| RH-CALL-07 | Does a call to a name missing from the cached catalog refresh once before failing? | Yes. A tool the upstream added since caching succeeds | integration |
| RH-CALL-08 | Does a policy change take effect on the next call? | Yes. Rules are not cached | e2e |
| RH-CALL-09 | Is the upstream called at all when a call rule denies? | No | e2e |
| RH-CALL-10 | Does a kill switch rule refuse every call to that tool? | Yes | e2e |

## Elicitation

| ID | Question | Expected | Level |
| --- | --- | --- | --- |
| RH-ELI-01 | A client that supports elicitation calls a guarded tool. What comes back? | `resultType: "input_required"` with the gateway's input request | e2e |
| RH-ELI-02 | A client without elicitation calls it. What comes back? | An `isError` result. No hang | e2e |
| RH-ELI-03 | The user declines. What happens? | An `isError` result, and the upstream is not called | e2e |
| RH-ELI-04 | The user accepts. What happens? | The call proceeds to the upstream | e2e |
| RH-ELI-05 | A client sends an "accept" with no gateway state. Accepted? | No. Rejected | e2e |
| RH-ELI-06 | A client replays valid state with changed arguments. Accepted? | No. The argument hash does not match | e2e |
| RH-ELI-07 | State older than five minutes. Accepted? | No | unit |
| RH-ELI-08 | State issued to another caller. Accepted? | No | unit |
| RH-ELI-09 | Does elicitation work on `prompts/get` and `resources/read`? | Yes | e2e |
| RH-ELI-10 | Is the upstream called before the gateway's prompt resolves? | No | e2e |
| RH-ELI-11 | Are capabilities read from the current request, not inferred from earlier ones? | Yes | e2e |
| RH-ELI-12 | Is the gateway's state a signed envelope with the upstream state inside? | Yes. Tampering is rejected | unit |
| RH-ELI-13 | Does `elicit()` fall back to an error when the client cannot elicit, without a second rule? | Yes | e2e |

## Upstream input

| ID | Question | Expected | Level |
| --- | --- | --- | --- |
| RH-INP-01 | Does an upstream `input_required` reach the client? | Yes, with the upstream state wrapped | e2e |
| RH-INP-02 | Can `tools/elicit` deny it? | Yes. The client receives an error result | e2e |
| RH-INP-03 | Can `tools/elicit` reword the message? | Yes, for example "Jira asks: " prepended | e2e |
| RH-INP-04 | Does `tools/result` run on an `input_required`? | No | e2e |
| RH-INP-05 | On the retry, does the upstream receive only its own answers and state? | Yes | e2e |

## Results

| ID | Question | Expected | Level |
| --- | --- | --- | --- |
| RH-RES-01 | Does `drop()` remove three named fields? | Yes, including inside arrays | unit |
| RH-RES-02 | Does `dropSchema()` remove the same properties from `outputSchema`? | Yes | unit |
| RH-RES-03 | Does `content: []` drop the text copy of structured output? | Yes | e2e |
| RH-RES-04 | Does native `map` projection keep only chosen fields? | Yes | unit |
| RH-RES-05 | Does `slice` cap an array? | Yes | unit |
| RH-RES-06 | Is there runtime validation against `outputSchema`? | No. A mismatched shaped result passes | e2e |
| RH-RES-07 | Does an upstream that breaks its own schema pass through? | Yes | e2e |
| RH-RES-08 | Does a `resource_link` a result rule adds get the mux prefix? | Yes. The prefix runs last | e2e |
| RH-RES-09 | Does a deny in `tools/result` return an error result? | Yes | e2e |

## Server hooks

| ID | Question | Expected | Level |
| --- | --- | --- | --- |
| RH-SRV-01 | Does the upstream see `bifrost` as the client by default? | Yes | e2e |
| RH-SRV-02 | With elicitation off in `server/connect`, does the upstream see no elicitation capability? | Yes | e2e |
| RH-SRV-03 | With `tools` switched off, are tools absent from listings and calls unknown? | Yes | e2e |
| RH-SRV-04 | With `prompts` or `resources` switched off, are they absent? | Yes | e2e |
| RH-SRV-05 | With `instructions` off, are instructions hidden? | Yes | e2e |
| RH-SRV-06 | Does `server/modify` rewrite instructions on 1:1? | Yes | e2e |
| RH-SRV-07 | Are instructions fetched live, so an upstream change shows at once? | Yes | e2e |
| RH-SRV-08 | Does discover advertise `listChanged: false`? | Yes | e2e |
| RH-SRV-09 | Are completions and logging left unadvertised? | Yes | e2e |
| RH-SRV-10 | Does the `progress` switch control forwarding of upstream progress? | Yes | e2e |
| RH-SRV-11 | Does `get_server_instructions(slug)` return the full text on the mux? | Yes, after `server/modify` | e2e |
| RH-SRV-12 | Are mux instructions slug-labelled excerpts with a budget and a truncation mark? | Yes | e2e |

## Prompts

| ID | Question | Expected | Level |
| --- | --- | --- | --- |
| RH-PRM-01 | Does `prompts/filter` hide a prompt from the list and from `prompts/get`? | Yes | e2e |
| RH-PRM-02 | Does a renamed prompt reach the upstream by its original name? | Yes | e2e |
| RH-PRM-03 | Is a `prompts/get` deny a JSON-RPC error with the reason? | Yes | e2e |

## Resources and templates

| ID | Question | Expected | Level |
| --- | --- | --- | --- |
| RH-RSC-01 | Does `resources/filter` hide a resource and refuse its read? | Yes | e2e |
| RH-RSC-02 | Can `resources/modify` change a URI? | No. Saving fails | unit |
| RH-RSC-03 | Can `templates/modify` change `uriTemplate`? | No. Saving fails | unit |
| RH-RSC-04 | Can `resources/modify` change the title? | Yes | e2e |
| RH-RSC-05 | Is a `resources/read` deny a JSON-RPC error? | Yes | e2e |
| RH-RSC-06 | A template allows a URI that sits under a denied skill folder. Readable? | No. Any applicable deny wins | e2e |
| RH-RSC-07 | Is a URI that no listing or template covers unknown? | Yes, -32602 | e2e |
| RH-RSC-08 | Does a directory read drop hidden children? | Yes | e2e |
| RH-RSC-09 | Does an opt-in `resources/result` rule rewrite text? | Yes | e2e |
| RH-RSC-10 | Is a read result never cached, so a second read reaches the upstream? | Yes | e2e |

## Skills

| ID | Question | Expected | Level |
| --- | --- | --- | --- |
| RH-SKL-01 | Does denying a skill by name block its whole folder? | Yes, every file under it | e2e |
| RH-SKL-02 | Is a nested skill denied with its parent? | Yes | e2e |
| RH-SKL-03 | Are `name` and `description` shortcuts available in `skills/filter`? | Yes | unit |
| RH-SKL-04 | Two skills share a name. Does a `uri` rule deny only one? | Yes | e2e |
| RH-SKL-05 | Is a file under a denied folder refused when read through a broad template? | Yes | e2e |
| RH-SKL-06 | Does `resources/list` drop entries under a denied folder? | Yes | e2e |
| RH-SKL-07 | Does a rewrite of a skill file recompute its digest and size? | Yes | e2e |
| RH-SKL-08 | Is rewritten frontmatter copied into the entry? | Yes | e2e |
| RH-SKL-09 | Is a rewrite that changes `name` rejected for that skill? | Yes. Upstream bytes are kept | e2e |
| RH-SKL-10 | Is denying one file inside a skill allowed, leaving the manifest alone? | Yes | e2e |
| RH-SKL-11 | Does the mux prefix leave digests unchanged? | Yes | e2e |
| RH-SKL-12 | Does `skills/filter` deny a skill from `skills/list` and `skills/get`? | Yes | e2e |

## Mux URIs and fallback resolution

| ID | Question | Expected | Level |
| --- | --- | --- | --- |
| RH-MUX-01 | Do resource listings carry `<slug>+<scheme>` URIs on the mux? | Yes | e2e |
| RH-MUX-02 | Do templates keep their variables under the prefix? | Yes | e2e |
| RH-MUX-03 | Does a read strip the prefix before reaching the upstream? | Yes | e2e |
| RH-MUX-04 | Do `contents[].uri`, `resource_link` and embedded resources get the prefix? | Yes | e2e |
| RH-MUX-05 | Do skill entries and manifest URIs get the prefix? | Yes | e2e |
| RH-MUX-06 | Does `docs+git+ssh://x` split into `docs` and `git+ssh://x`? | Yes | unit |
| RH-MUX-07 | Are URIs unchanged on 1:1? | Yes | e2e |
| RH-MUX-08 | Is a prefix naming a slug the caller does not hold refused before any upstream call? | Yes | e2e |
| RH-MUX-09 | Does an unprefixed URI with one exact match resolve, and return the prefixed URI? | Yes | e2e |
| RH-MUX-10 | Does an ambiguous unprefixed URI return -32602 listing the candidates in the message and `data`? | Yes | e2e |
| RH-MUX-11 | Does an unknown unprefixed URI return -32602 "not found"? | Yes | e2e |
| RH-MUX-12 | Does a template match count only when it is the only match? | Yes | e2e |
| RH-MUX-13 | Is the URI never sent to upstreams that do not list it? | Yes | e2e |
| RH-MUX-14 | Are candidates limited to the caller's slugs after filters? | Yes | e2e |
| RH-MUX-15 | Does fallback fetch listings on a cache miss? | Yes | integration |
| RH-MUX-16 | Is the slug grammar `[a-z][a-z0-9]*`? | Yes. A leading digit, `-`, `_`, `+` or `.` is rejected | unit |
| RH-MUX-17 | Is the unavailable-server meta still reported on mux listings? | Yes | integration |

## Search mode

| ID | Question | Expected | Level |
| --- | --- | --- | --- |
| RH-SRC-01 | Does `tool_search` rank exposed tools, so a renamed tool is found by its new name and a hidden tool is not found? | Yes | e2e |
| RH-SRC-02 | Does `list_tools` group by slug with tool names indented? | Yes | e2e |
| RH-SRC-03 | Does `get_tool_definition` return the exposed definition? | Yes | e2e |
| RH-SRC-04 | Does `execute_tool` go through call rules, reversal and result rules? | Yes | e2e |

## Freshness and caching

| ID | Question | Expected | Level |
| --- | --- | --- | --- |
| RH-FRS-01 | Do list results carry `ttlMs` and `cacheScope: "private"`? | Yes | e2e |
| RH-FRS-02 | Does the gateway send `list_changed` notifications? | No | e2e |
| RH-FRS-03 | Are tool results cached? | No. Two identical calls reach the upstream twice | e2e |
| RH-FRS-04 | Is upstream `_meta` reduced to the allowlist at fetch? | Yes | e2e |
| RH-FRS-05 | Is the catalog cached, so a listing does not refetch? | Yes | integration |

## Limits

| ID | Question | Expected | Level |
| --- | --- | --- | --- |
| RH-LIM-01 | Is an upstream response over the size cap rejected? | Yes, with "exceeds gateway size limit" | e2e |
| RH-LIM-02 | Does a rule over the CEL cost limit deny? | Yes | unit |
| RH-LIM-03 | Does exceeding the per-request time budget deny? | Yes | unit |

## Performance and scale

| ID | Question | Expected | Level |
| --- | --- | --- | --- |
| RH-PRF-01 | Is a rule reading `caller` classified dynamic, and one reading only item fields static? | Yes | unit |
| RH-PRF-02 | Do rules over the real Datadog catalog (about 500 tools) and 20 rules run within the budget? | Yes | e2e |
| RH-PRF-03 | Do all real Datadog and harness tool shapes pass through filter and modify with no changes intact? | Yes. Output equals input when no rule matches | e2e |

## Adversarial cases from the Codex review

Codex reviewed the design and this catalogue and proposed 52 scenarios. They are in `docs/rules/adversarial-codex.md`.

Proven in `pkg/hooks` (scenario or Go test carries the ID): ADV-02, ADV-03, ADV-07, ADV-10, ADV-11, ADV-12, ADV-13, ADV-14, ADV-15, ADV-16, ADV-17, ADV-18, ADV-19, ADV-20, ADV-21, ADV-22, ADV-23, ADV-24, ADV-26, ADV-27, ADV-28, ADV-29, ADV-30, ADV-31, ADV-32, ADV-33, ADV-37, ADV-38, ADV-39, ADV-43, ADV-44, ADV-45, ADV-46, ADV-47, ADV-51.

Deferred to gateway integration, because they concern catalog caching, single flight, pagination, task dispatch, quotas, transport or cancellation: ADV-01, ADV-04, ADV-05, ADV-06, ADV-08, ADV-09, ADV-25, ADV-34, ADV-35, ADV-36, ADV-40, ADV-41, ADV-42, ADV-49, ADV-50, ADV-52.

Not applicable to the engine: ADV-48. Skill frontmatter is parsed by one parser, used for both the manifest and rewriting.

## Decisions made while building the engine

These came out of implementing the cases above and are now part of the design:

- Listings are evaluated per item in parallel and then reduced in one step, which checks collisions and sorts output by slug a-z, then name a-z.
- `resources/filter` runs on every URI that is read, listed or not, so a single file reachable only through a template or a skill can be denied.
- URIs with dot segments, empty segments or percent-encoded unreserved characters are refused as not canonical, so an upstream cannot resolve a path differently from the gateway.
- Skill rewriting is skipped for rules that read `caller`, `client` or `now`, because list time and read time would produce different bytes. It also skips files the caller cannot read, and drops manifest entries outside the skill's folder.
- Upstream input requests use the spec's wire shape, `{method: "elicitation/create", params: {...}}`. Rules see `request.message` and the other params, plus `request.key` and `request.method`.
- Gateway state carries earlier answers, so several confirmations in a row complete.
- Pins win over call-rule patches. A call-rule patch that names a renamed parameter's upstream key fails closed.
- Client arguments are limited to 64 levels of nesting, duplicate JSON keys are rejected, and large integers are forwarded exactly.
- Results are checked against the size cap again after rules shape them.
- Setting a `meta.*` field to `null` removes the key.

