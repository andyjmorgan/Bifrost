The largest gaps are identity-scoped catalog caching, authorization across continuations, incomplete fallback discovery, and schema transformations that change more than property names. Several scenarios expose missing policy, rather than just implementation bugs.

Below, `hook: condition → assignments` abbreviates the JSON rule wrapper. Rules are enabled and enforced unless marked `audit`. Omitted rules mean no rules. Upstream spies should record every request so tests can assert both returned output and absence of unauthorized upstream calls.

Severity is ranked by potential consequence; “Critical” means the stated conditions can produce an authorization bypass.

1. **ADV-01 — Critical: Caller-dependent catalogs contaminate a shared cache.**  
   **Attacker/condition:** An upstream varies its catalog by authenticated identity.  
   **Setup:** Two users share one server record but use different upstream credentials. Admin’s `tools/list` returns `public_search` and `admin_export`; member’s returns only `public_search`. Warm the per-server cache as admin, then list and call `admin_export` as member. Repeat with a server that tailors its catalog through forwarded request metadata.  
   **Expected:** Design is silent about credential scope in catalog keys. It should require identity-scoped catalogs when upstream visibility varies, or explicitly prohibit that configuration. A member must not inherit admin-only definitions or routing.  
   **Target:** Catalog fetch/cache; `server/connect`; call step 2.

2. **ADV-02 — Critical: Narrowed mux routes do not actually isolate execution.**  
   **Attacker/condition:** Member holds `docs` and `eng` but operates through `/mcp?slugs=docs`.  
   **Setup:** On that endpoint, call `eng__export`, read `eng+file:///secret`, request `skills/get` for an `eng+skill:` URI, and use `execute_tool` or `get_server_instructions` for `eng`. Also read an unprefixed URI listed only by `eng`.  
   **Expected:** Narrowing is presented as an isolation mechanism. Every path, including fallback and synthetic tools, must restrict itself to `docs`; no `eng` lookup or upstream request. Checking grants alone is insufficient.  
   **Target:** Route scope resolution before all hooks.

3. **ADV-03 — Critical: A revoked grant survives inside valid elicitation state.**  
   **Attacker/condition:** Member retains a signed challenge after losing access.  
   **Setup:** Obtain confirmation state for `docs__delete_page`, revoke the `docs` grant, then retry unchanged arguments with an accepted answer before expiry. Keep policy generation unchanged.  
   **Expected:** Current entitlement authorization must run before accepting the continuation. No upstream call. State proves challenge integrity, not continuing authorization.  
   **Target:** Request authorization; elicitation retry validation.

4. **ADV-04 — Critical: Task APIs bypass tool visibility policy.**  
   **Attacker/condition:** Member knows a task handle created through a now-hidden tool.  
   **Setup:** Enable tasks. Create a task for `export`, then hide `export` or switch tools off while keeping tasks on. Attempt every supported task status/result/cancel operation with its handle. Separately switch tasks off and repeat.  
   **Expected:** Design accepts shared-credential handle visibility, but is silent about policy enforcement on task operations. It should specify whether tool restrictions govern task results and cancellation. `tasks:false` must block all task operations.  
   **Target:** `server/config`; task dispatch and task-result delivery.

5. **ADV-05 — Critical: Permission changes mid-request produce a mixed policy.**  
   **Attacker/condition:** Concurrent administrative change.  
   **Setup:** Block a call after `tools/filter` permits `export`. Publish a policy generation that denies `export`, then release execution into `tools/call` and upstream dispatch. Repeat with grant revocation.  
   **Expected:** Design is silent about in-flight semantics. It should define an authorization linearization point and an immutable policy snapshot per request; no mixture of generations. If immediate revocation is promised, recheck before dispatch.  
   **Target:** Request-wide policy snapshot; steps 1–6.

6. **ADV-06 — Critical: Shared single-flight work runs with the wrong caller context.**  
   **Attacker/condition:** Concurrent cold-cache requests with different upstream identities.  
   **Setup:** Admin and member request a cold catalog simultaneously. Pause admin’s fetch until member joins the same per-server single flight. Upstream returns identity-specific catalogs and records credentials and forwarded `_meta`.  
   **Expected:** Design is silent on single-flight partitioning. Requests with different catalog authorization or connection-shaping contexts must not share a fetch that exposes one caller’s view to another.  
   **Target:** Catalog single flight; `server/connect`.

7. **ADV-07 — High: Incomplete fallback discovery creates a false unique match.**  
   **Attacker/condition:** One upstream fails during fallback resolution.  
   **Setup:** `docs` and `eng` both list `file:///runbook`. Cache `docs`; make `eng` cold and return HTTP 503 for its listing. Read unprefixed `file:///runbook`.  
   **Expected:** “One confident match” cannot be established while an eligible upstream remains unknown. Design is silent about the failure response; it should return an incomplete-resolution error, not route to `docs`.  
   **Target:** Fallback candidate collection.

8. **ADV-08 — High: A stale positive fallback match prevents ambiguity detection.**  
   **Attacker/condition:** Catalog freshness differs across slugs.  
   **Setup:** Cache `docs` containing `file:///runbook` and `eng` without it. Add the same URI to `eng`. Read the unprefixed URI before cache expiry, then after expiry.  
   **Expected:** Design permits cached listings but does not define the confidence/freshness boundary. Specify whether fresh cached absence is authoritative and exactly when all eligible catalogs refresh. Once `eng` is expired, it must not be skipped merely because `docs` already matches.  
   **Target:** Fallback freshness and fetch-on-miss semantics.

9. **ADV-09 — High: Pagination hides a denying skill ancestor.**  
   **Attacker/condition:** Denied skill appears on a later catalog page.  
   **Setup:** Skills page 1 contains allowed `skill://team/public/SKILL.md`; page 2 contains parent `skill://team/SKILL.md`, denied by URI. A broad resource template covers both folders. Cold-read `skill://team/public/data.txt` before any `skills/list`.  
   **Expected:** The denied parent must block the read. Catalog acquisition must discover applicable skill denies across pages before authorization, rather than treating page 1 as a complete catalog.  
   **Target:** Skill catalog loading; resource read step 3.

10. **ADV-10 — High: URI normalization bypasses denied folders.**  
    **Attacker/condition:** Client uses alternate spellings that upstream normalizes.  
    **Setup:** Deny `file:///skills/private/SKILL.md`; allow template `file:///{+path}`. Read `file:///skills/public/../private/key.txt`, `file:///skills/%70rivate/key.txt`, and `file:///skills/public/%2e%2e/private/key.txt`. Fixture upstream maps them to the denied file.  
    **Expected:** Design is silent on normalization. It should reject ambiguous aliases or apply an explicit upstream-compatible canonicalization before folder authorization. Raw prefix checks must not authorize these fixture requests.  
    **Target:** Resource/skill identity matching.

11. **ADV-11 — High: Read-result substitution returns a forbidden resource.**  
    **Attacker/condition:** Malicious upstream substitutes a different URI.  
    **Setup:** List allowed `file:///public` and denied `file:///secret`. Client reads public; upstream returns `contents:[{uri:"file:///secret",text:"SECRET"}]`, or both contents together.  
    **Expected:** Design checks the requested URI but is silent about returned content identities. It should authorize every returned content URI or reject mismatched results. Prefixing alone must not legitimize the forbidden item.  
    **Target:** Resource read steps 5–7.

12. **ADV-12 — High: Directory entries escape the authorized parent.**  
    **Attacker/condition:** Malicious directory response.  
    **Setup:** Read `docs+file:///public/`. Upstream returns children `file:///public/ok`, `file:///secret`, and `eng+file:///secret`; only the first belongs to the directory.  
    **Expected:** Existing hidden-child filtering does not establish parent membership. Design should require valid child containment, apply filters to each actual identity, and never interpret an upstream-supplied prefix as authority for `eng`.  
    **Target:** Directory-result validation and mux prefixing.

13. **ADV-13 — High: Native URI schemes collide with mux syntax.**  
    **Attacker/condition:** Legal upstream scheme contains `+`.  
    **Setup:** `docs` lists native URI `eng+file:///x`; caller also holds slug `eng`, which lists `file:///x`. Read unprefixed `eng+file:///x`, then explicit `docs+eng+file:///x`.  
    **Expected:** Design is silent about native schemes indistinguishable from mux prefixes. It should state that the first form addresses slug `eng` and the second addresses `docs`’ native URI, with no fallback reinterpretation after authorization failure.  
    **Target:** Mux URI parser; fallback boundary.

14. **ADV-14 — High: A call rule overrides a static pin.**  
    **Attacker/condition:** Careless administrator combines contradictory rules.  
    **Setup:** Modify pins `tenant` to `"A"`. Call rule sets `args.tenant` to `"B"`. Client omits tenant.  
    **Expected:** “Static value forced” implies `"B"` must not reach upstream. The final check explicitly mentions ranges, leaving pin revalidation unclear. Reject the conflict or reassert the pin under a documented precedence rule.  
    **Target:** Call steps 4–5.

15. **ADV-15 — High: A call patch bypasses the exposed-key reverse map.**  
    **Attacker/condition:** Careless rule or attacker-controlled map patch.  
    **Setup:** Rename upstream `tenant` to `account`. Call rule writes `args.tenant:"B"` while client supplies `account:"A"`. Repeat with a patch to a completely undeclared `admin` key.  
    **Expected:** Design is silent about unknown and original-key call patches. It should resolve patches through explicit parameter identities and reject ambiguous/undeclared keys; it must not forward both tenant identities or bypass hidden-key restrictions.  
    **Target:** Call patch validation; reversal.

16. **ADV-16 — High: Renaming properties leaves schema constraints attached to old names.**  
    **Attacker/condition:** Legal JSON Schema with cross-property constraints.  
    **Setup:** Tool schema has properties `cql`, `token`, `required:["cql"]`, `dependentRequired:{"cql":["token"]}`, and `if:{"required":["cql"]}`. Rename `cql` to `query`; list and call with `{"query":"x","token":"t"}`.  
    **Expected:** Design is silent about schema-wide identifier references. A reversible rename must preserve their meaning or explicitly reject unsupported schemas. Exposing `query` with a required nonexistent `cql` is incorrect.  
    **Target:** `tools/modify`; schema materialization.

17. **ADV-17 — High: Hiding a parameter does not hide it inside schema branches.**  
    **Attacker/condition:** Legal composed schema.  
    **Setup:** Root property `tenant` also appears in `allOf` and an `if/then` branch, with nested `required:["tenant"]`. Hide and pin tenant to `"A"`. List the tool, then send `tenant:"B"` through a branch-compatible argument object.  
    **Expected:** Client values must still be rejected. Design should define how hiding updates all relevant schema locations and whether unsupported compositions fail closed; deleting only the root property is insufficient.  
    **Target:** Parameter hiding; schema processing; call step 3.

18. **ADV-18 — High: Literal property names collide with overlay path syntax.**  
    **Attacker/condition:** Upstream uses legal unusual JSON keys.  
    **Setup:** Schema contains top-level properties `"a.b"`, `"a"`, `"*"`, `"x/y"`, and `""`; `a` is an object with property `b`. Attempt to rename or hide the literal `"a.b"` using the editor/API field representation.  
    **Expected:** Design is silent about escaping property paths. Define an unambiguous path encoding, or reject unaddressable edits explicitly. An edit to literal `"a.b"` must not alter nested `a.b`.  
    **Target:** Rule field parser; overlay keys; reverse maps.

19. **ADV-19 — High: Large JSON integers defeat enum or argument-hash identity.**  
    **Attacker/condition:** Client exploits float64 decoding.  
    **Setup:** Pin or enum `id` to integer `9007199254740992`. Submit `9007199254740993`. Separately obtain a challenge for the former and retry with the latter.  
    **Expected:** Values must remain distinct through JSON parsing, CEL conversion, validation, hashing, and serialization. Reject the second value; never accept it because both rounded to one float.  
    **Target:** Effective arguments; CEL numeric adapter; challenge hashing.

20. **ADV-20 — High: Duplicate JSON keys produce different authorization and execution values.**  
    **Attacker/condition:** Malicious client sends raw JSON.  
    **Setup:** Rule denies `args.space=="HR"`. Send literal request arguments `{"space":"ENG","space":"HR"}` and reverse the order. Repeat duplicate `name` and `requestState` members at the request level.  
    **Expected:** Design is silent on duplicate members. Reject them before policy evaluation, or guarantee one consistent interpretation across every layer. No parser disagreement may authorize ENG and execute HR.  
    **Target:** Front-door decoding; argument construction.

21. **ADV-21 — High: Multiple confirmations can never complete safely.**  
    **Attacker/condition:** Administrator requires two independent approvals.  
    **Setup:** First call rule elicits `confirmDelete` if absent; second elicits `confirmBilling` if absent. Accept the first, receive the second, then retry with only the second answer and then with both answers.  
    **Expected:** Design is silent about accumulating gateway challenges. It should preserve validated prior approvals in bound state or reject unsupported multi-challenge policies. It must neither loop indefinitely nor accept client-invented prior approvals.  
    **Target:** Sequential `elicit()` rules; continuation state machine.

22. **ADV-22 — High: Continuation state can move between methods or resource URIs.**  
    **Attacker/condition:** Client reuses valid state across surfaces.  
    **Setup:** Tool `delete`, prompt `delete`, and two resource URIs each require key `confirm`. Obtain state for the tool, then attach it to `prompts/get`. Obtain state for `resources/read(file:///a)` and retry against `file:///b` or `resources/directory/read`.  
    **Expected:** Design’s binding names an “upstream tool” but also supports other methods. State must bind method and complete target identity, including resource URI. All substitutions fail before upstream access.  
    **Target:** Challenge identity and state validation.

23. **ADV-23 — High: Post-patch argument hashing makes legitimate retries fail—or binds the wrong operation.**  
    **Attacker/condition:** Non-idempotent call patch precedes confirmation.  
    **Setup:** Rule 1 sets `args.n:args.n+1`; rule 2 elicits confirmation. Initial client arguments are `{"n":1}`. Retry the original request with accepted state.  
    **Expected:** Design is silent about whether the hash covers pre-rule or challenge-time arguments and where evaluation resumes. Define deterministic retry semantics: execute `n:2` once after approval, not `n:3`, and do not reject an unchanged original request merely because patches ran twice.  
    **Target:** Effective-argument hashing; call-rule replay.

24. **ADV-24 — High: One upstream input request poisons a multi-request challenge.**  
    **Attacker/condition:** Malicious upstream returns mixed input requests.  
    **Setup:** Return three requests: allowed key `a`, denied key `b`, allowed key `c`. Add input rule `request.key=="b" → decision:"deny"`. Repeat with duplicate keys and a key named `bifrost.confirm`.  
    **Expected:** A deny must produce an error for the operation, not a partial challenge that upstream interprets differently. Design should additionally define duplicate-key rejection and enforce gateway namespace separation.  
    **Target:** `tools/elicit`; input batch validation; state wrapping.

25. **ADV-25 — High: Snapshot changes redirect a name to a different upstream tool.**  
    **Attacker/condition:** Catalog refresh races with reversal.  
    **Setup:** Version V1 exposes upstream `safe` as `run`; V2 exposes upstream `dangerous` as `run`. Pause a call after V1 lookup; refresh the cache to V2; resume reversal and dispatch.  
    **Expected:** The call must retain V1’s item pointer and reverse map throughout. It must never authorize `safe` and execute `dangerous`.  
    **Target:** Catalog snapshot lifetime; call steps 2–6.

26. **ADV-26 — High: Parent-object reads ignore child overlays.**  
    **Attacker/condition:** Chained rule reads an entire edited object.  
    **Setup:** Modify rule 1 changes `params.q.description` to `"NEW"`. Rule 2 sets tool description from `inputSchema.properties.q.description`; rule 3 inspects `inputSchema.properties` through a comprehension. Upstream description is `"OLD"`.  
    **Expected:** Both later rules must see `"NEW"`. Exact-path fallback alone is insufficient when CEL retrieves a parent map first; overlay reads must compose descendants consistently.  
    **Target:** Copy-on-write read semantics.

27. **ADV-27 — High: Whole-object replacement leaves stale child overlays.**  
    **Attacker/condition:** Mixed parent and child updates.  
    **Setup:** Result rule 1 sets `structuredContent` to `{"a":{"x":1}}`; rule 2 replaces it with `{"b":2}`; rule 3 builds `content` from current `result.structuredContent`. Separately test renaming a parameter after changing its description.  
    **Expected:** Later parent replacement must supersede earlier descendants, and rename must carry prior edits. No resurrected fields or stale reads.  
    **Target:** Overlay precedence; result chaining; rename materialization.

28. **ADV-28 — High: Concurrent overlays leak between entitlements.**  
    **Attacker/condition:** Two entitlements share one cached server object.  
    **Setup:** Entitlement `a` renames `query` to `q` and pins tenant `"A"`; `b` keeps `query` and pins tenant `"B"`. Run interleaved listings and calls through both with barriers during serialization.  
    **Expected:** Each response and upstream call must retain its own names and pins. The shared catalog and the other entitlement’s overlay must remain untouched.  
    **Target:** Overlay ownership; concurrent serialization and reversal.

29. **ADV-29 — High: Skill manifests smuggle files outside the skill root.**  
    **Attacker/condition:** Malicious skill entry.  
    **Setup:** `skill://public/SKILL.md` lists manifest resources `skill://public/a.txt`, `skill://private/key.txt`, and `https://external.example/x`. Enable a skill text-rewrite rule that causes manifest files to be read during listing.  
    **Expected:** Design is silent about validating manifest membership and routing these internal reads. Reject invalid/out-of-root entries under a defined policy; do not fetch arbitrary URLs or bypass resource filters because the read originated from skill rewriting.  
    **Target:** Skills manifest validation; internal rewrite reads.

30. **ADV-30 — High: A denied file is fetched by the skill-rewrite pipeline.**  
    **Attacker/condition:** Administrator denies a file but enables broad rewriting.  
    **Setup:** Skill manifest contains `secret.txt`; `resources/filter` denies that exact URI. `resources/result: true → text:slugUris(result.text)` applies to the skill. Request `skills/list`.  
    **Expected:** Design permits unpruned manifests but does not authorize reading denied files internally. It should explicitly apply resource authorization before rewrite fetches and define whether the skill is rejected or retains an unchanged manifest entry.  
    **Target:** Skill rewrite orchestration; resource-filter reuse.

31. **ADV-31 — High: A skill rewrite partially commits before its name change is rejected.**  
    **Attacker/condition:** Multi-file rewrite fails after earlier files succeed.  
    **Setup:** Manifest order is `guide.txt`, then `SKILL.md`. Rewrite the guide successfully; rewrite frontmatter name from `widgets` to `other`.  
    **Expected:** “Rejected for that skill, which keeps its upstream bytes” requires rollback of every file, digest, size, and entry change for the skill. No hybrid rewritten/original skill.  
    **Target:** Skill rewrite transaction boundary.

32. **ADV-32 — High: Synthetic search tools collide with upstream names.**  
    **Attacker/condition:** Malicious upstream chooses reserved-looking names.  
    **Setup:** Direct search route serves upstream tools named `tool_search`, `execute_tool`, `get_tool_definition`, and `get_server_instructions`. Call synthetic tools, then execute each upstream tool through `execute_tool`.  
    **Expected:** Design is silent about namespace reservations. Synthetic dispatch must be unambiguous, upstream names must not replace gateway implementations, and underlying tools must remain addressable or be rejected explicitly.  
    **Target:** Search-mode tool registration and dispatch.

33. **ADV-33 — High: Request metadata forges the verified caller.**  
    **Attacker/condition:** Member controls client `_meta`.  
    **Setup:** Rule denies `export` unless `caller.role=="admin"` and `caller.clientId=="trusted"`. Authenticate as member/untrusted client, but send `_meta` containing `caller`, `role`, `clientId`, and client-info fields claiming those values.  
    **Expected:** Only authenticated identity supplies `caller.*`. No generic metadata merge may override it. The call remains denied.  
    **Target:** Common-variable construction; authenticated identity boundary.

34. **ADV-34 — High: Progress crosses requests with reused identifiers.**  
    **Attacker/condition:** Shared upstream connection and overlapping calls.  
    **Setup:** Two users issue calls with the same JSON-RPC request ID and progress token. Upstream emits progress for each, late progress after completion, and unsolicited progress without a matching call. Enable progress only for one entitlement.  
    **Expected:** Forward only on the originating request’s stream and only when its switch permits. Late/unmatched events are dropped; IDs must be scoped to connection/request identity, not globally.  
    **Target:** Progress routing; `server/config`.

35. **ADV-35 — High: Pagination loops evade per-response limits.**  
    **Attacker/condition:** Malicious catalog pagination.  
    **Setup:** Each page is under the size cap. Upstream repeatedly returns cursor `"next"` with new items, or alternates `"a"` and `"b"`. Client issues one listing or cold call.  
    **Expected:** Design’s per-response cap does not bound total catalog work. Specify cycle detection, page/item/aggregate-byte limits, and fetch deadlines. Fail clearly without publishing a partial catalog as complete.  
    **Target:** Catalog pagination and limits.

36. **ADV-36 — High: Small compressed responses expand beyond safe bounds.**  
    **Attacker/condition:** Malicious upstream transport encoding.  
    **Setup:** Send a gzip-compressed catalog of 20 KB that expands to 200 MB, with configured response cap 1 MB. Repeat using chunked transfer without `Content-Length`.  
    **Expected:** The cap must apply to bytes actually decoded for processing, enforced while streaming. Reject before allocating the full response.  
    **Target:** Upstream transport; response-size enforcement.

37. **ADV-37 — High: Deep nesting exhausts the stack before CEL limits apply.**  
    **Attacker/condition:** Small but deeply nested JSON.  
    **Setup:** Return a schema or structured result with 20,000 nested arrays/objects, still below the byte cap. Exercise pass-through, `drop`, serialization, and preview.  
    **Expected:** Design is silent about structural depth. Enforce a documented depth limit with a controlled error; no panic, process crash, or unbounded recursion before CEL metering.  
    **Target:** JSON decode; recursive helpers; serializer.

38. **ADV-38 — High: Policy output amplification bypasses upstream size caps.**  
    **Attacker/condition:** Careless or hostile rule writer.  
    **Setup:** Upstream returns a 1 KB text result. A sequence of individually cheap result rules doubles the current text until output exceeds 100 MB. Repeat with tool descriptions and mux instructions.  
    **Expected:** Design caps upstream responses, not generated output. It should also cap intermediate and serialized gateway output, accounting for amplification before allocation becomes excessive.  
    **Target:** Rule output materialization; final response limits.

39. **ADV-39 — High: Audit work becomes an enforcement denial.**  
    **Attacker/condition:** Expensive audit-only rules.  
    **Setup:** An allowed tool has many audit rules, each below its CEL cost limit, collectively exceeding the request time budget. Compare the same request with audit rules disabled.  
    **Expected:** Design contains conflicting requirements: audit failures do not change outcomes, but request-budget exhaustion denies. Define separate audit budgeting or explicit precedence. An audit-only rollout should not silently become an availability policy.  
    **Target:** Audit evaluation; aggregate rule budget.

40. **ADV-40 — High: Quota counters permit concurrent overrun or charge retries twice.**  
    **Attacker/condition:** Concurrent clients and elicitation retries.  
    **Setup:** Configure `limit` for one operation per minute with a fixed caller-scoped key. Release 50 calls simultaneously. Separately place the limit before an elicitation rule and complete the confirmation retry.  
    **Expected:** Design leaves counter storage and charging semantics open. Specify atomic admission and whether challenges, denied calls, and retries consume quota. Under a one-execution policy, at most one upstream execution occurs, and confirmation must not consume a second execution allowance.  
    **Target:** `tools/call.limit`; retry accounting.

41. **ADV-41 — Medium: One failed page publishes a truncated catalog.**  
    **Attacker/condition:** Partial upstream failure during refresh.  
    **Setup:** Cache complete V1 with 100 tools. V2 page 1 returns 50 tools; page 2 fails. List and call a V1 tool absent from page 1.  
    **Expected:** Design is silent about refresh publication. Publish only complete catalog generations; either retain explicitly permitted stale V1 or mark the server unavailable. Never replace V1 with an apparently complete 50-tool catalog.  
    **Target:** Catalog refresh transaction and unavailable-server reporting.

42. **ADV-42 — Medium: Client pagination cursors survive policy or caller changes.**  
    **Attacker/condition:** Client reuses a gateway listing cursor.  
    **Setup:** Obtain page 1 and its cursor as admin. Reuse as member; separately change policy to hide remaining tools before fetching page 2.  
    **Expected:** Design is silent about downstream pagination. Cursors must not preserve old authorization or expose another caller’s view. Bind them to appropriate context and snapshot, or rerun current rules with documented restart semantics.  
    **Target:** Gateway listing pagination.

43. **ADV-43 — Medium: Unicode normalization aliases two distinct identifiers.**  
    **Attacker/condition:** Distinct strings normalize to the same visual name.  
    **Setup:** Rename two tools to `"café"` using U+00E9 and `"café"` using U+0065 U+0301. Call both exact spellings. Repeat for parameter names if accepted by the gateway’s name grammar.  
    **Expected:** Design is silent about normalization. Either preserve exact code-point identity end to end or normalize before collision checking and lookup. Never list two identities but route both to one upstream tool.  
    **Target:** Identifier validation; collision detection; lookup.

44. **ADV-44 — Medium: YAML-style search output permits structural injection.**  
    **Attacker/condition:** Malicious catalog text or administrator rename.  
    **Setup:** Attempt a tool name containing `"\neng:\n  - admin_export"` and names containing `:`, `#`, or leading `-`. Request search `list_tools`. Where names are invalid under the front-door grammar, supply them upstream to test rejection.  
    **Expected:** Validate protocol name constraints; quote/escape every accepted identifier in the rendered format. One tool must not appear as another slug’s tool or alter grouping.  
    **Target:** Search list renderer; upstream identifier validation.

45. **ADV-45 — Medium: `dropSchema` breaks references or mutates shared definitions.**  
    **Attacker/condition:** Legal output schema uses reusable definitions.  
    **Setup:** `issues.items` and `archived.items` both reference `#/$defs/Issue`; `Issue` requires `self` and `title`. Drop only `"issues.*.self"` from result and schema. Also test recursive `$ref` within `Issue`.  
    **Expected:** Design is silent on reference-aware schema edits. Remove the corresponding requirement without altering `archived`, or reject unsupported transformations clearly. No dangling references, accidental global deletion, or recursive expansion loop.  
    **Target:** `dropSchema`; preview validation.

46. **ADV-46 — Medium: Boolean and alternative schemas break parameter modification.**  
    **Attacker/condition:** Legal nontrivial schema shapes.  
    **Setup:** Use object input schema with properties `anything:true`, `never:false`, and `choice:{"oneOf":[{"type":"string"},{"type":"integer"}]}`. Add a description/default/enum to each through modify rules.  
    **Expected:** Design is silent about supported schema transformations. Preserve boolean-schema semantics where possible or return an explicit modification error; never panic or silently convert `false` into an accepting schema.  
    **Target:** Parameter modification; JSON Schema adapters.

47. **ADV-47 — Medium: Time-dependent skill rewrites never match their advertised digest.**  
    **Attacker/condition:** Administrator uses `now` in a content rewrite.  
    **Setup:** `resources/result: true → text:result.text + string(now)` applies to a static skill file. List the skill, advance the test clock, then read the file.  
    **Expected:** Design is silent about deterministic bytes across listing and read. No content cache plus time-dependent rewriting can make every digest stale. It should reject nondeterministic rewrites for hashed skills or define a reproducible rewrite context shared by manifest and read.  
    **Target:** Skill hashing; dynamic result rules; freshness contract.

48. **ADV-48 — Medium: Frontmatter parsing disagrees between rewrite and manifest generation.**  
    **Attacker/condition:** Malformed or ambiguous rewritten YAML.  
    **Setup:** Rewrite `SKILL.md` with duplicate `name` keys, `name: null`, a YAML alias for `name`, a UTF-8 BOM, or CRLF delimiters. Keep the URI’s skill name `widgets`.  
    **Expected:** Design only explicitly addresses changed names. It should define a single supported frontmatter parser and reject invalid/ambiguous rewrites atomically. Entry frontmatter and delivered bytes must be interpreted consistently.  
    **Target:** Skill rewrite validation; frontmatter extraction.

49. **ADV-49 — Medium: Protocol-version mismatch reaches application dispatch.**  
    **Attacker/condition:** Legacy or malformed client.  
    **Setup:** Negotiate a version other than `2026-07-28`, omit required version context on a subsequent request, or provide contradictory transport/request version declarations. Then invoke tools and send old-style elicitation responses.  
    **Expected:** The front door supports only the stated version. Define the exact rejection/negotiation behavior and reject incompatible messages before upstream dispatch; do not silently interpret old input semantics as current answers.  
    **Target:** Front-door protocol validation.

50. **ADV-50 — Medium: Cancellation or timeout leaves an executable continuation.**  
    **Attacker/condition:** Client cancels while upstream work or input generation is racing.  
    **Setup:** Start a call, cancel it while the upstream is about to return `input_required`, then allow that response to arrive. Repeat with a configured call timeout. Attempt to use any resulting continuation state.  
    **Expected:** Design is silent about cancellation and late responses. Define when cancellation becomes final; do not emit a fresh usable challenge after termination or automatically replay a timed-out operation. Upstream side effects already committed cannot be assumed undone.  
    **Target:** Call lifecycle; timeout; upstream input wrapping.

51. **ADV-51 — Medium: Zero, negative, or overflowing timeout values change control flow.**  
    **Attacker/condition:** Careless administrator supplies an extreme CEL result.  
    **Setup:** Call rules set timeout to zero, a negative duration, and a duration exceeding Go’s supported representation. Follow an earlier valid timeout rule with each invalid value.  
    **Expected:** Design does not specify the field’s type or bounds. Define them and reject invalid values before upstream dispatch; no overflow to an unlimited or unexpectedly long timeout.  
    **Target:** Typed hook output validation; call deadline construction.

52. **ADV-52 — Medium: Connection reuse preserves another request’s upstream capabilities.**  
    **Attacker/condition:** Entitlements share an upstream connection but shape connection fields differently.  
    **Setup:** Entitlement `a` advertises elicitation and forwards real client identity; `b` disables elicitation and stays anonymous. Connect through `a`, then call through `b` using the same server connection pool. Reverse the order.  
    **Expected:** Design says connection fields are shaped in place but is silent about negotiated connection reuse. Partition connections by relevant negotiated context or renegotiate safely. `b` must not inherit `a`’s client identity or advertised input capability.  
    **Target:** `server/connect`; upstream session pooling.