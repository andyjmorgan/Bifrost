# Rules engine v2

Written November 15, 2026. v2 implements the fix pass agreed in `codex-review-response.md` and `codex-final-comments.md`, plus the performance work. It is uncommitted on branch `rules-hooks` and not wired into the gateway.

## Results

- 208 of 208 scenarios pass: the original 119, Codex's 70 and 19 new v2 scenarios.
- Every Go test passes under the race detector, including all 17 of Codex's tests. The other packages in the repo also pass.
- Fuzzers ran for 30 to 60 seconds each with no findings left: `FuzzCodexParse`, `FuzzCodexDecodeArgs`, `FuzzCodexURI` and a new `FuzzScenarioLoader`. `FuzzCodexParse` found two real cases during the pass, `[]]` and `null\v`, which are fixed and kept as seeds.
- Changed expectations in existing scenarios come only from decisions recorded below, never from matching the implementation.

Reproduce from the repository root:

```sh
go build -o bin/hookctl ./cmd/hookctl
./bin/hookctl run -summary pkg/hooks/testdata/scenarios/*.json
go test ./pkg/hooks/ -race -count=1
go test ./pkg/hooks/ -run '^$' -fuzz '^FuzzCodexParse$' -fuzztime 60s
go test ./pkg/hooks/ -run '^$' -bench . -benchmem
```

## Known weak spots

Read these first. They are what we would attack next.

1. Calls under a policy that renames tools still evaluate the whole catalog: about 30 to 50 ms on 1,481 tools. The single-tool shortcut only applies when no enforce rule sets `name`. A proper fix evaluates only the name-changing rules across the catalog.
2. A `then` that runs `regex.replace` on every tool costs about 26 µs per tool. The `ext.Regex` function may compile its pattern on every call; Bifrost's old helper cached patterns. Not investigated.
3. Budget accounting sums rule time across parallel goroutines, so a parallel listing spends its enforcement budget faster than wall-clock time passes. That fails closed, but a large catalog with an expensive policy could hit the 2 s default sooner than expected.
4. Schemas using composition keywords (`allOf`, `anyOf`, `oneOf`, `if`, `$ref` and so on) do not get `additionalProperties: false` enforcement. An issue is recorded instead.
5. A deny on an unsupported URI template applies to every URI under its literal prefix, which can over-deny. This is the conservative side of Codex's any-deny-wins requirement.
6. A skill's rewrite plan is recomputed on every file read: the rules run over all of the skill's text files each time. That is correct but costly for large skills.
7. Elicitation state is not single-use. That waits for the KV store, which the gateway checks before the engine sees the state.
8. Benchmarks were taken with a machine load average of 18 from other work. Repeated runs agree within about 10%, but absolute numbers are noisy.
9. Gateway integration items remain deferred: catalog caching and refresh, single flight, pagination, task dispatch, quotas, transport size limits and cancellation (ADV-01, 04, 05, 06, 08, 09, 25, 34, 35, 36, 40, 41, 42, 49, 50, 52; RH-CALL-07, RH-MUX-15, RH-MUX-17, RH-FRS-05, RH-SRV-07, RH-SRV-10).

## Performance

Real corpus, 1,481 tools, the 20-rule policy in `hooks_test.go`:

| Benchmark | Before | After |
| --- | --- | --- |
| List, no rules | 66 ms, 29.6 MB, 484,000 allocations | 6.0 ms, 4.0 MB, 42,000 allocations |
| List with the policy, one worker | 190 ms, 78 MB, 1.27 million allocations | 72 to 79 ms, 30 MB, 433,000 allocations |
| List with the policy, parallel | 34 ms | 15 ms |
| Prepare one tool | 41 µs (JSON round trip) | 3.8 µs, cached for the tool's lifetime |
| Build the output for a changed tool | 44 µs | 8.6 µs |
| One call under the policy | 48 ms, 80 MB | 30 to 50 ms, 35 to 46 MB |
| One simple rule | 1.2 µs | 1.2 µs |

What changed:

- Each catalog tool is prepared once and cached against its own pointer, with cleanup when the tool is garbage collected. Views reference the tool's own values, so untouched schema parts keep their pointer identity (CX-G04 passes through `ListTools`).
- Output tools copy the struct and replace only changed fields. No JSON round trip.
- Derived fields (`annotations`, `params`, `meta`) are memoized per item and recomputed only after a change.
- Calls evaluate only the called tool when no enforce rule can rename a tool.
- The bounded evaluation context is created once per item rather than once per rule.

## Findings

| Finding | Status | Fix |
| --- | --- | --- |
| F01 exact numbers | Fixed, narrow | Pin, enum and range comparisons use exact values. Schema constants are no longer rounded, because tools are no longer converted through float64 |
| F02 effective arguments | Fixed | Visible pins and defaults of added parameters are present when call rules run |
| F03 hidden added parameters | Fixed | Origin, name and visibility are tracked separately. Hidden added parameters are refused and always stripped |
| F04 reply arguments | Fixed | `CallContext` carries the exact forwarded arguments to every reply hook. `prompts/input` gets the arguments as sent upstream |
| F05, F07, F16, F17 visibility | Fixed | One per-request view is used by listings, reads, directory children, returned content and fallback candidates. Template denies now also apply to listings |
| F06 template matching | Fixed | `{var}` and `{+var}` with exact literals, empty expansions allowed. Unsupported forms never grant, and their denies cover their literal prefix |
| F08 binary results | Fixed | Result rules run for every content item. A deny blocks the whole result |
| F09 cache scope | Fixed | Read and directory results carry the gateway `ttlMs` and `cacheScope: "private"` |
| F10 size limits | Fixed | One finisher caps every reply and listing after shaping, including `input_required` |
| F11 helper cost | Fixed | `drop` and `dropSchema` are charged to CEL's cost tracker by input size times paths, with a hard cap of 100,000 nodes per call checked before copying |
| F12 deadline at every return | Fixed | Every success and forward path checks the budget last |
| F13 audit budget | Fixed | Accounted time with separate enforcement and audit allowances. Audit exhaustion is "would error" and never changes an outcome |
| F14, F19, F20, F21, F22 skills | Fixed | One all-or-nothing plan per skill drives both entry and reads. Empty files handled, the name check uses the root `SKILL.md` only, dynamic skills are never rewritten, strict frontmatter |
| F15 trailing JSON | Fixed | Only JSON whitespace may follow the root value, for rules and arguments |
| F18 boolean schemas | Fixed | Untouched booleans stay booleans. A changed `false` becomes `allOf: [false]` plus the change |
| F23, F24 search names | Fixed | One formatter: prefixed only on the mux, exact case-sensitive lookup |
| F25 dropSchema wildcard | Fixed | A trailing `*` removes every property. A path through `$ref` is an error |
| F26 `result.meta` | Fixed | Read-only alias of `_meta` |
| F27 discover | Fixed | `supportedVersions`, the Skills extension declaration, identity in `_meta["io.modelcontextprotocol/serverInfo"]` |
| F28 copy-on-write in the pipeline | Fixed | See Performance |
| F29 audit-supplied parameters | Fixed | Audit rules never count. After call rules, hidden required parameters must actually have a value |
| F30, F31 runner | Fixed | `execute_tool` continuation fields; exact number comparison in expectations |
| F32 profiler nil set | Fixed | Nil-safe accessor |
| F33, F34 upstream input forms | Fixed | Dispatch on `resultType`. Answers are forwarded without upstream state. State-only replies are wrapped |
| F35 ordering | Decided | URI order for resources, templates and skills; name order for tools and prompts; slug a-z first. Byte consistency is tested on the shuffled real corpus |
| F36 integral numbers | Fixed | Integrality comes from the exact received value. `1`, `1.0` and `1e0` are ints in CEL |
| A01 reserved escapes | Decided | `%2F` stays opaque. CX-19 now asserts the forward with the URI unchanged |
| D01 state binding | Fixed | Entitlement id, the method of directory reads, and a generation combining the gateway's generation with a fingerprint of the compiled rules |
| D02 answers into upstream rounds | Fixed | The validated answers travel in `CallContext` into the wrapped state |
| Replay | Deferred | KV store, validated by the gateway before the engine |

## Additional items

| Item | Status |
| --- | --- |
| 1. State leaks upstream names | Deferred to the KV store's opaque handle, as agreed |
| 2. Consent text spoofing | `elicit(key, template, values, error?)` quotes, bounds and escapes values |
| 3. Forged mux sections | Every excerpt line is quoted with `"> "` |
| 4. Rule names in refusals | Clients get "The call to X was refused by policy." Names stay in hits |
| 5. `limit` silently ignored | Refused at compile time |
| 6. Calls scan the catalog | Shortcut when no rule renames; see weak spot 1 |
| 7. Empty generation | Generation always includes the rule fingerprint, so it is never empty and changes with the rules |
| 8. Missing capabilities | `client.capabilities` is always present |
| 9. `slice` past the end | The example changes to `size(xs) > n ? xs.slice(0, n) : xs`; no helper |
| `server/config` errors | All eight switches go off |
| Search metadata | Search uses `server/modify` title and description |
| MIME type | Read correctly from the content item |
| Disabled rules | Compile problems become `Set.Warnings`, never blocking |
| `additionalProperties: false` | Enforced with `patternProperties`, added parameters and composed-schema handling as agreed |
| `server/connect` in forwards | Every forward carries the connect result, and the `_meta` policy is applied |
| Fallback events | `Outcome.fallback` reports URI, method, outcome, slug, candidate count and template use |

## Weak tests strengthened

- Generation binding uses a real issued token and a per-step generation change, plus a rule-change case.
- RH-CALL-08 replaces the rule set between two calls.
- `dropSchema` through `$ref` asserts an error.
- The hail mary asserts the fallback event, the prefixed reply URI, that the prefixed URI then works directly, ambiguous and template outcomes, and directory reads through fallback.
- New: byte consistency on the shuffled real corpus, an exact-outcome oracle for real-call policy cases, and fuzzing of the scenario loader.
- RH-SRV-07 and RH-SRV-10 are reclassified as gateway integration. The real-call policy test is relabelled as a smoke test, alongside the new oracle.
