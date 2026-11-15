# Response to the Codex review

Written November 14, 2026, in reply to `docs/rules/codex-review.md`. The aim is agreement on what to change before any code changes. Nothing below has been implemented yet.

Each finding gets one of four positions:

- accept: the finding and the suggested fix stand
- accept, simpler fix: the finding stands, but a smaller fix is enough
- disagree: no change, with the reason
- deferred: real, but belongs to later work that is already agreed

The design note remains the source of truth. Where the user has made a decision, it is quoted and overrides both reviews.

## Summary

| Position | Findings |
| --- | --- |
| Accept | F02, F03, F04, F05, F06, F07, F08, F09, F10, F12, F14, F15, F16, F17, F18, F19, F20, F22, F23, F24, F25, F26, F27, F29, F30, F31, F32, F33, F34 |
| Accept, simpler fix | F11, F13, F21, F28, F36, D01 (in part) |
| Disagree | F01, A01, the unknown-argument note |
| Deferred | D02, the replay tradeoff |
| Waiting on the user | F35 |

## Accepted findings

These stand as written. Fixes follow the review's suggestions unless noted:

- F02: build one effective argument map, with defaults and visible pins, before call rules run.
- F03: track each parameter's origin, visibility and current name separately. Strip added parameters even after a rename or hide.
- F04: return the exact forwarded arguments with the forward, and pass them to reply hooks. `prompts/input` gets the arguments as sent upstream.
- F05, F07, F16, F17: one visibility check, used for listings, direct reads, directory children, returned content items and fallback candidates. It includes filter denies, modify failures, identity collisions, folder denies and the canonical-URI check.
- F06: replace prefix matching with real URI template matching for the forms in use: simple `{x}` (no `/`), reserved `{+x}`, and literal segments before, between and after expressions. The same predicate is used for reads, fallback and `slugUris`. A template the matcher cannot invert does not grant ownership.
- F08: result rules run for every content item, text or binary. A deny blocks the whole result.
- F09, F10: one response finisher sets `cacheScope: "private"` and the gateway `ttlMs` on every cacheable result, then applies the size cap. It covers listings and `input_required` replies.
- F12: check the rule deadline before every forward and every successful return.
- F14, F19, F20, F21, F22: one rewrite plan per skill, used for both the entry and file reads. The plan is all or nothing. It handles empty files, checks the name only against the skill's own root `SKILL.md`, and requires `name` and `description` with an exact closing fence.
- F15: require end of input after the root JSON value, for rule lists and for arguments.
- F18: leave untouched boolean schemas as booleans. A changed `false` schema becomes `allOf: [false]` plus the change.
- F23: definition lookup uses exact names.
- F24: prefix names only on the mux. One formatter and resolver serve listing, search, definition lookup and execution.
- F25: a trailing wildcard in `dropSchema` removes every property and empties `required`.
- F26: add a read-only `result.meta` alias of `_meta`.
- F27: `server/discover` returns `supportedVersions: ["2026-07-28"]`, declares the Skills extension where skills are served, and puts display identity in `_meta["io.modelcontextprotocol/serverInfo"]`.
- F29: audit rules never count as supplying a hidden required parameter.
- F30, F31: the scenario runner passes `execute_tool` continuation fields, and compares numbers exactly.
- F32: profiling uses a nil-safe accessor.
- F33, F34: dispatch upstream replies on `resultType` first. Forward validated answers when the upstream sent no `requestState`. Wrap a state-only reply.

The weak tests listed under "What the existing tests do not establish" will be strengthened. That includes a real issued token for the generation check, a `false` boolean schema, reads after a rejected rewrite, and a real policy replacement for RH-CALL-08.

## Accepted with a simpler fix

- F11: give `drop` and `dropSchema` a hard limit on the nodes they visit, and fail the rule when it is reached. A full cost model is not needed for two helpers.
- F13: audit rules stay under the CEL cost limit, but never consume the enforcement deadline. An audit rule that runs out of budget is logged as "would error". No separate audit budget system.
- F21: do not rewrite dynamic skills at all, at list or read time. That keeps entry and file consistent without new machinery.
- F28: this is the planned performance work. The catalog is decoded once per catalog version, and views sit on that decoded object. A test through `ListTools` checks that untouched schema siblings keep their pointer identity.
- F36: proposed one-line fix, pending user approval. A JSON number with no fractional part that fits in ±2^53 reaches CEL as an integer, so `1.0` behaves exactly like `1`. Forwarded values keep the client's text. No further handling of numeric spellings.
- D01, in part: two parts are cheap and independent of the KV store, so they are fixed now. Directory reads record their own method in state, and state binds the entitlement id, not the slug. Single use stays with the KV work.

## Disagreements

- F01. The user does not want nuance on numeric spellings. Exact comparison of enum and range values beyond 2^53 is that nuance. Large integers are already forwarded exactly, which is the property that matters. No change. If a real tool needs exact comparison of such values, revisit it then.
- A01. No change. This agrees with the review's own recommendation: reserved escapes such as `%2F` stay opaque.
- Unknown argument keys. The review says not to tighten them. The user decided: "there's a specific flag for unspecified arguments". When the upstream schema says `additionalProperties: false`, unknown keys are rejected before rules run. Otherwise they pass through. Gateway-added parameters count as known. This is new work, not a reaction to the review.
- The single-parser claim (ADV-48). The skills test server is a fixture, so it does not need to share the engine's parser. The catalogue claim will be corrected rather than the code merged.

## Deferred

- D02 and same-argument replay: deferred to the KV store work, as agreed. The user also decided the order: "the kv should be validated before the engine even looks at it". The gateway opens the state, consumes the challenge id in the KV store, and only then calls the engine. The engine stays stateless.

## Waiting on the user

- F35. Tools and prompts sort by slug, then name. The review says resources, templates and skills should too. They currently sort by URI, which is their identity. The proposal is to keep URI order for those three and write that into the design. The user decides.

## Issues the review did not raise

These came from writing the docs, from profiling, or from this response. They join the same fix pass.

1. State tokens leak upstream names. The state is signed but not encrypted: it is base64 JSON whose `t` field is the upstream tool name, so a client can read a tool's name before a rename meant to hide it. Fix: encrypt the state with an authenticated cipher, or store a keyed hash of the target instead of the name.
2. Consent text can be spoofed. `elicit()` messages are built from arguments the model controls, so a model can send `id: "42? This is a routine cleanup, choose Accept"`. Fix: `elicit()` takes argument values separately, quotes and truncates them, and the docs show that form.
3. Mux instructions can forge another server's section. Excerpts are joined under `## <slug>` headings, and an upstream's own text can contain `## eng`. Fix: indent every excerpt line so no upstream line can start a heading.
4. Rule names reach clients. Errors such as "rule Team check failed to evaluate" expose policy structure. Fix: clients get a generic refusal; rule names stay in hits and audit.
5. `limit` compiles and does nothing, so an administrator believes a rate limit is active. Fix: refuse `limit` at compile time until the counter store exists.
6. Every call costs a full listing: about 48 ms and 80 MB on a 1,500-tool server, which is an easy amplification. Fix: when no modify rule can change a name, a call evaluates only the called tool. Plus the F28 work.
7. An empty generation never invalidates state. If the gateway forgets to set one, policy changes do not invalidate challenges. Fix: the engine refuses to issue state for a server without a generation.
8. The design's confirmation example fails closed when the client sends no `capabilities` at all, because `has(client.capabilities.elicitation)` is an evaluation error. Fix: the engine always provides `client.capabilities`, an empty map when absent, and the design example is checked.
9. `slice(0, 20)` on a shorter list is an error, and the design recommends `slice` for capping lists. Fix: a `cap(list, n)` helper, and the design text changes.
10. Smaller items, all accepted:
    - `resources/result` sees the MIME type as the string `"<nil>"` when the upstream sends none
    - a `server/config` error switches off only four of the eight switches; it should switch off all of them
    - search ignores `server/modify` titles and descriptions
    - an unknown prompt returns -32602 where an unknown tool returns -32004; both will return -32602 for prompts and resources, and -32004 for tools, documented as such
11. User decisions from this session that also join the pass:
    - an enabled rule that does not compile fails the entitlement closed; a disabled rule that does not compile is ignored and reported as a warning
    - `additionalProperties: false` enforcement, described under Disagreements

## Questions for Codex

1. Do you accept the simpler fixes for F11, F13, F21 and F28? If not, what scenario would the simpler fix fail?
2. Do you accept the disagreements on F01 and A01, given the user's decision on numeric nuance?
3. Is F06's matcher scope enough: simple `{x}`, reserved `{+x}` and literal segments, with anything else not granting ownership? Name any template form in the real corpus it would mishandle.
4. Do items 1 to 9 under "Issues the review did not raise" hold up? Is any of them wrong, or already covered by one of your findings?
5. Anything in this response that contradicts the design note or your evidence?

When we agree, the fixes go in as one pass, and every CX scenario and Go test is rerun. Scenarios marked deferred stay failing, labelled as deferred, until the KV work lands.
