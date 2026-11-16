Bifrost hooks adversarial review — November 14, 2026

Verdict: the current standalone engine does not yet meet the v3 design. The existing suite passes, but the new tests reproduce policy, resource-visibility, skill-integrity and protocol failures. Every confirmed finding below includes a suggested fix. A failing test count is not a count of independent bugs.

This review concerns [the rules-hooks checkout](</home/localuser/source/bifrost>). It does not claim a deployed gateway vulnerability: the library is not integrated. No engine or upstream-server implementation files were edited by this review. No commits, pushes, gateway changes or real-data writes were made. The actively edited docs/rules/hooks/ folder was excluded.

The sources of truth are the [v3 design](</home/localuser/vaults/Datadog/AI Platform/AI Gateway/MCP Gateway - Policy Engine Intent.md:375>), [test catalogue](</home/localuser/source/bifrost/docs/rules/test-cases.md:1>), [earlier adversarial catalogue](</home/localuser/source/bifrost/docs/rules/adversarial-codex.md:1>), [2026-07-28 protocol schema](</tmp/claude-1000/-home-localuser-source/8d398a5e-5d6f-467d-ac35-93454d14a2af/scratchpad/spec/2026-07-28.ts:1>) and [Skills specification](</tmp/claude-1000/-home-localuser-source/48e610cd-bab1-4887-ba5e-31a56aa0adf9/scratchpad/ext-skills/skills.mdx:1>). Protocol requirements take their actual strength: MUST requirements, SHOULD recommendations and optional fields are not interchangeable.

The original go test ./pkg/hooks/ -race -count=1 passed in 33.769 seconds. The freshly built CLI passed all 119 original scenarios, with no skips. The final combined CLI run has 132 passes and 57 failures: 119 original passes, plus 13 passes and 57 failures among 70 new scenarios. No original scenario regressed. No flakiness or race report was observed in the runs performed; that is not a proof that all schedules are race-free.

The final full race run and test details are recorded in [the review evidence JSON](</home/localuser/source/bifrost/docs/rules/codex-review-results.json>). There are 17 new top-level Go tests: 13 fail and four pass. The stronger parallel entitlement/reversal test passes. Two JSON fuzz seed checks fail on malformed input accepted by the parsers; these are deterministic seed failures, not long fuzz discoveries. URI fuzzing completed 30,137 executions without a detected violation of its limited round-trip/panic invariants. The upstream Skills server race suite passed in 1.597 seconds.

Client compatibility is part of the proposed fixes. [CX-68](</home/localuser/source/bifrost/pkg/hooks/testdata/scenarios/90-codex-adversarial.json:4503>), [CX-69](</home/localuser/source/bifrost/pkg/hooks/testdata/scenarios/90-codex-adversarial.json:4578>) and [CX-70](</home/localuser/source/bifrost/pkg/hooks/testdata/scenarios/90-codex-adversarial.json:4695>) confirm valid array/scalar/null structuredContent and untouched composed/boolean schemas pass through. [CX-G16](</home/localuser/source/bifrost/pkg/hooks/codex_adversarial_test.go:331>) confirms 1, 1.0, 1e0, 1E+0 and 0.1e1 remain equivalent values, while their original argument tokens are preserved. Strings and booleans remain distinct from numbers. Fixes must preserve these results. A client that rounds a number before sending it cannot be repaired by the gateway; the gateway must still avoid introducing new rounding or accepting a different value during authorization.

That compatibility control covers JSON decoding and equality. The additional end-to-end policy test [CX-G17](</home/localuser/source/bifrost/pkg/hooks/codex_adversarial_test.go:353>) exposes a separate problem: equivalent integer spellings become different CEL types and produce different arithmetic outcomes. F36 gives the compatibility-preserving fix; rejecting valid 1.0 or 1e0 at the JSON boundary would be the wrong fix.

Elicitation persistence, replay policy and storage redesign are deferred at the user’s request because elicitation is moving to a KV store. D01 and D02 retain present-code regression evidence for that work. F33 and F34 concern valid protocol message shapes, and F04 concerns actual forwarded arguments; they remain relevant regardless of the storage mechanism.

Confirmed findings follow in severity order. “High” identifies a reproducible authorization, integrity, resource-bound or crash failure in the library. “Medium” identifies a reproducible correctness, interoperability or test-evidence defect. “Low” identifies output ordering.

F01 · High · Exact JSON numbers lose their identity during policy checks

Source: [tools.go:510](</home/localuser/source/bifrost/pkg/hooks/tools.go:510>), [tools.go:607](</home/localuser/source/bifrost/pkg/hooks/tools.go:607>), [tools.go:57](</home/localuser/source/bifrost/pkg/hooks/tools.go:57>).

Required behavior: The design requires enum/range enforcement and exact forwarding of large integers (tools/call; Decisions made while building).

Verified behavior: An enum containing only 9007199254740992 accepts 9007199254740993. Minimum and maximum checks also collapse these values. Nested enum values have the same problem. Editing an unrelated description rounds an upstream schema const of 9007199254740993 down to 9007199254740992. Proof: [CX-01](</home/localuser/source/bifrost/pkg/hooks/testdata/scenarios/90-codex-adversarial.json:3>), [CX-02](</home/localuser/source/bifrost/pkg/hooks/testdata/scenarios/90-codex-adversarial.json:63>), [CX-03](</home/localuser/source/bifrost/pkg/hooks/testdata/scenarios/90-codex-adversarial.json:123>), [CX-04](</home/localuser/source/bifrost/pkg/hooks/testdata/scenarios/90-codex-adversarial.json:183>), [CX-G12](</home/localuser/source/bifrost/pkg/hooks/codex_adversarial_test.go:285>).

Suggested fix: Keep JSON numbers exact in toolMap and schema materialization. Compare JSON numbers by mathematical value with a bounded exact decimal/integer representation, recursively for enums. Preserve equality of 1, 1.0 and 1e0; do not compare only their bytes or coerce strings into numbers. Preserve values during forwarding. For values CEL cannot represent, return a defined evaluation error when a rule needs that value instead of silently rounding it.

F02 · High · Call rules do not see all effective arguments

Source: [tools.go:482](</home/localuser/source/bifrost/pkg/hooks/tools.go:482>), [tools.go:422](</home/localuser/source/bifrost/pkg/hooks/tools.go:422>), [engine.go:348](</home/localuser/source/bifrost/pkg/hooks/engine.go:348>).

Required behavior: Call rules judge effective arguments before execution; pins force values and defaults fill missing arguments.

Verified behavior: When the client omits a visible pinned tenant, the tenant is absent during policy evaluation and appears only before forwarding. A deny conditioned on that tenant is bypassed. Defaults on added parameters disappear because defaults are recorded only for nonempty upstream identities. Proof: [CX-05](</home/localuser/source/bifrost/pkg/hooks/testdata/scenarios/90-codex-adversarial.json:245>), [CX-06](</home/localuser/source/bifrost/pkg/hooks/testdata/scenarios/90-codex-adversarial.json:315>).

Suggested fix: Build one effective exposed argument map containing defaults and visible pins before evaluating call rules. Keep added-parameter defaults under their own identities. Preserve the existing rejection of conflicting client pin values, and reassert pins after patches before final validation and reversal.

F03 · High · Hiding an added parameter permits it to reach the upstream

Source: [tools.go:425](</home/localuser/source/bifrost/pkg/hooks/tools.go:425>), [tools.go:482](</home/localuser/source/bifrost/pkg/hooks/tools.go:482>).

Required behavior: Hidden parameters reject client values. Added parameters are removed before forwarding.

Verified behavior: Adding internal and then hiding it removes it from both Added and Hidden. Supplying internal:"injected" is treated as an unknown extra property and forwarded. Proof: [CX-07](</home/localuser/source/bifrost/pkg/hooks/testdata/scenarios/90-codex-adversarial.json:384>).

Suggested fix: Track parameter origin, visibility and current name independently. Keep tombstones for hidden added parameters, reject their client keys, and strip every added identity during reversal even after rename or hide.

F04 · High · Reply hooks inspect different arguments from those sent upstream

Source: [server.go:299](</home/localuser/source/bifrost/pkg/hooks/server.go:299>), [resources.go:196](</home/localuser/source/bifrost/pkg/hooks/resources.go:196>).

Required behavior: tools/elicit and tools/result receive args as sent upstream. Prompt input has the corresponding request context.

Verified behavior: A call patch changes n from 1 to 2, and forwarding sends 2, but result/input rules read 1. prompts/elicit receives an empty argument map, even after argument renames and patches. Proof: [CX-09](</home/localuser/source/bifrost/pkg/hooks/testdata/scenarios/90-codex-adversarial.json:498>), [CX-10](</home/localuser/source/bifrost/pkg/hooks/testdata/scenarios/90-codex-adversarial.json:569>), [CX-11](</home/localuser/source/bifrost/pkg/hooks/testdata/scenarios/90-codex-adversarial.json:653>).

Suggested fix: Return an immutable call execution context alongside Forward, containing the exact upstream args, original item identity, exposed view and policy snapshot. Pass that context into reply handlers. Do not recompute arguments from the client request or rerun non-idempotent patches.

F05 · High · A resource hidden by the listing pipeline can still be read

Source: [resources.go:696](</home/localuser/source/bifrost/pkg/hooks/resources.go:696>), [resources.go:521](</home/localuser/source/bifrost/pkg/hooks/resources.go:521>), [resources.go:564](</home/localuser/source/bifrost/pkg/hooks/resources.go:564>).

Required behavior: Hidden items must be unreadable; evaluation errors fail closed; applicable denies are final.

Verified behavior: Resources and templates hidden by a modify evaluation error remain readable. Duplicate resource identities are hidden by listItems collision handling, but readCheck still selects the raw catalog resource and forwards the read. Proof: [CX-12](</home/localuser/source/bifrost/pkg/hooks/testdata/scenarios/90-codex-adversarial.json:743>), [CX-13](</home/localuser/source/bifrost/pkg/hooks/testdata/scenarios/90-codex-adversarial.json:807>), [CX-14](</home/localuser/source/bifrost/pkg/hooks/testdata/scenarios/90-codex-adversarial.json:871>).

Suggested fix: Use one resolved visibility result for list, direct read, directory filtering and fallback. It must include filter decisions, modify failures and identity collisions. Preserve legitimate template-only resources by applying the same template visibility checks without requiring a concrete resource listing.

F06 · High · Template matching authorizes URIs outside the template

Source: [uris.go:258](</home/localuser/source/bifrost/pkg/hooks/uris.go:258>), [resources.go:735](</home/localuser/source/bifrost/pkg/hooks/resources.go:735>), [resources.go:505](</home/localuser/source/bifrost/pkg/hooks/resources.go:505>).

Required behavior: A URI must be covered by a listing or a matching RFC 6570 template. slugUris rewrites only URIs owned by that upstream.

Verified behavior: file:///teams/{team}/public/{file} authorizes file:///teams/blue/private/key. A simple {file} accepts raw slash-separated descendants. slugUris ignores template suffixes and rewrites a /private URI for a template ending in /public. Proof: [CX-17](</home/localuser/source/bifrost/pkg/hooks/testdata/scenarios/90-codex-adversarial.json:1073>), [CX-18](</home/localuser/source/bifrost/pkg/hooks/testdata/scenarios/90-codex-adversarial.json:1115>), [CX-44](</home/localuser/source/bifrost/pkg/hooks/testdata/scenarios/90-codex-adversarial.json:2719>).

Suggested fix: Use a tested RFC 6570-aware matcher that respects every literal segment and operator, including the distinction between {x} and {+x}. Where inversion is unsupported, do not infer ownership from a prefix. Reuse the same ownership predicate in fallback, read authorization and slugUris.

F07 · High · Directory MIME type bypasses resource and template filters

Source: [resources.go:858](</home/localuser/source/bifrost/pkg/hooks/resources.go:858>).

Required behavior: Directory reads apply the same visibility check to each child and drop hidden children.

Verified behavior: Children marked inode/directory receive only the denied-skill-folder check. A directory denied by resources/filter or templates/filter is still returned. Proof: [CX-20](</home/localuser/source/bifrost/pkg/hooks/testdata/scenarios/90-codex-adversarial.json:1207>), [CX-21](</home/localuser/source/bifrost/pkg/hooks/testdata/scenarios/90-codex-adversarial.json:1264>).

Suggested fix: Run resource filters, applicable template filters and skill ancestor denies for every child regardless of MIME type. Separate directory existence/containment checks from visibility so valid directory-only entries are still supported.

F08 · High · Binary resource data bypasses a result deny

Source: [resources.go:886](</home/localuser/source/bifrost/pkg/hooks/resources.go:886>).

Required behavior: resources/result can deny a read result. The MCP resource content model supports both text and blob values.

Verified behavior: A rule with when:true and decision:"deny" is never evaluated for a blob content item. The reply returns the binary data successfully. Proof: [CX-64](</home/localuser/source/bifrost/pkg/hooks/testdata/scenarios/90-codex-adversarial.json:4282>).

Suggested fix: Run result decision rules for every content item, including blobs. Permit text mutation only when text is present, preserving blob bytes and MIME type otherwise. An unconditional deny must block the whole result for either representation.

F09 · High · Read responses retain public cache scope after caller-specific policy

Source: [resources.go:907](</home/localuser/source/bifrost/pkg/hooks/resources.go:907>).

Required behavior: Every gateway CacheableResult must carry gateway ttlMs and cacheScope:"private". The protocol permits public results to be shared across authorization contexts.

Verified behavior: Resource and directory read replies retain the upstream cacheScope:"public" and ttlMs:99999 despite gateway ttlMs:123. Results can depend on caller-specific filters or shaping. Proof: [CX-32](</home/localuser/source/bifrost/pkg/hooks/testdata/scenarios/90-codex-adversarial.json:2086>), [CX-33](</home/localuser/source/bifrost/pkg/hooks/testdata/scenarios/90-codex-adversarial.json:2142>).

Suggested fix: Finalize all CacheableResult variants through one response helper that sets private scope and the configured gateway TTL. Apply it after shaping and before size checks. Do not add cache fields to result types that are not CacheableResult.

F10 · High · Listing and input-required outputs bypass size limits

Source: [engine.go:200](</home/localuser/source/bifrost/pkg/hooks/engine.go:200>), [resources.go:292](</home/localuser/source/bifrost/pkg/hooks/resources.go:292>), [server.go:333](</home/localuser/source/bifrost/pkg/hooks/server.go:333>).

Required behavior: The design caps listings/results and checks size again after shaping.

Verified behavior: Oversized tool, resource and skill listings succeed. A tiny tool list expanded by a modify rule also succeeds. Input message shaping can produce a response larger than MaxResponseBytes without rejection. Proof: [CX-34](</home/localuser/source/bifrost/pkg/hooks/testdata/scenarios/90-codex-adversarial.json:2193>), [CX-35](</home/localuser/source/bifrost/pkg/hooks/testdata/scenarios/90-codex-adversarial.json:2239>), [CX-36](</home/localuser/source/bifrost/pkg/hooks/testdata/scenarios/90-codex-adversarial.json:2292>), [CX-37](</home/localuser/source/bifrost/pkg/hooks/testdata/scenarios/90-codex-adversarial.json:2335>), [CX-38](</home/localuser/source/bifrost/pkg/hooks/testdata/scenarios/90-codex-adversarial.json:2393>).

Suggested fix: Apply a final serialized-output cap to every response branch, including input_required and listings, after mux prefixes and state wrapping. Bound intermediate rule output too. The transport adapter must separately enforce upstream raw/decompressed limits before allocation; catalog-only scenarios cannot prove that transport boundary.

F11 · High · Custom helpers are effectively unmetered

Source: [cel.go:76](</home/localuser/source/bifrost/pkg/hooks/cel.go:76>), [cel.go:114](</home/localuser/source/bifrost/pkg/hooks/cel.go:114>).

Required behavior: Every helper must have a cost estimate feeding the CEL cost limit.

Verified behavior: drop over one row and over 10,000 rows both report an actual CEL cost of 3. The helper performs recursive copying outside meaningful cost accounting. Proof: [CX-G08](</home/localuser/source/bifrost/pkg/hooks/codex_adversarial_test.go:220>).

Suggested fix: Register runtime overload cost estimators based on visited input nodes and paths, plus compile-time estimates where supported. Bound traversal, depth and allocation before performing the work. Add interrupt checks within long helper traversals so a single helper cannot monopolize the request budget.

F12 · High · Expired rule contexts can still return success or forward

Source: [resources.go:90](</home/localuser/source/bifrost/pkg/hooks/resources.go:90>), [resources.go:764](</home/localuser/source/bifrost/pkg/hooks/resources.go:764>), [eval.go:61](</home/localuser/source/bifrost/pkg/hooks/eval.go:61>).

Required behavior: Exceeding the request rule budget is an evaluation error that denies.

Verified behavior: With an already expired rule context, prompts/get and resources/read forward, and prompt/resource listings succeed after simple rules. CEL interruption checks during comprehensions do not enforce every deadline. Proof: [CX-G07](</home/localuser/source/bifrost/pkg/hooks/codex_adversarial_test.go:196>).

Suggested fix: Check the shared rule deadline before and after each evaluation and at every final success/forward boundary. Propagate the same budget across the operation. Distinguish this enforcement deadline from the separate bounded audit budget described in F13.

F13 · High · Audit work can change an allowed call into a denial

Source: [engine.go:332](</home/localuser/source/bifrost/pkg/hooks/engine.go:332>), [eval.go:84](</home/localuser/source/bifrost/pkg/hooks/eval.go:84>).

Required behavior: Audit matches and failures must not change outcomes. The design also imposes a request budget; its precedence needs an explicit rule.

Verified behavior: The baseline forwards. Adding only an audit rule that exceeds the five-millisecond rule budget produces an isError refusal. This reproduces three consecutive times. Proof: [CX-G06](</home/localuser/source/bifrost/pkg/hooks/codex_adversarial_test.go:180>).

Suggested fix: Give audit evaluation a separate bounded budget and record its exhaustion as an audit error, without consuming the enforcement deadline. Specify how incomplete audit traces are represented after exhaustion; an unconditional complete-log promise cannot coexist with finite limits.

F14 · High · Skill read bytes contradict rewrite rejection and advertised digests

Source: [resources.go:360](</home/localuser/source/bifrost/pkg/hooks/resources.go:360>), [resources.go:401](</home/localuser/source/bifrost/pkg/hooks/resources.go:401>), [resources.go:886](</home/localuser/source/bifrost/pkg/hooks/resources.go:886>).

Required behavior: Request-dependent skill rewrites are skipped. A name-changing rewrite is rejected atomically for the whole skill, preserving upstream bytes.

Verified behavior: skills/get keeps original digests for clock/caller-dependent rules, while resources/read applies those rules. After a rejected name rewrite, reading SKILL.md still returns the changed name. A supporting file is also rewritten despite the skill-wide rollback. Proof: [CX-24](</home/localuser/source/bifrost/pkg/hooks/testdata/scenarios/90-codex-adversarial.json:1421>), [CX-25](</home/localuser/source/bifrost/pkg/hooks/testdata/scenarios/90-codex-adversarial.json:1507>), [CX-26](</home/localuser/source/bifrost/pkg/hooks/testdata/scenarios/90-codex-adversarial.json:1593>), [CX-27](</home/localuser/source/bifrost/pkg/hooks/testdata/scenarios/90-codex-adversarial.json:1676>).

Suggested fix: Centralize the skill rewrite plan and use it for manifest construction and file reads. A skipped or rejected plan must serve original bytes for every file. Validate all files and root frontmatter before accepting a plan; recompute from live content per request, consistent with the no-content-cache design.

F15 · High · Malformed rule documents can silently discard the denying policy

Source: [rule.go:70](</home/localuser/source/bifrost/pkg/hooks/rule.go:70>), [engine.go:390](</home/localuser/source/bifrost/pkg/hooks/engine.go:390>).

Required behavior: The rules wrapper is JSON and invalid rule lists fail closed. Arguments must be one JSON object.

Verified behavior: Parse accepts [] followed by a second denying rule array, or by garbage, and returns an empty policy. decodeArgs accepts multiple JSON documents such as {} {} and uses only the first. Fuzz seed checks reproduce both. Proof: [CX-G13](</home/localuser/source/bifrost/pkg/hooks/codex_adversarial_test.go:300>), [CX-G14](</home/localuser/source/bifrost/pkg/hooks/codex_adversarial_test.go:308>), [FuzzCodexDecodeArgs](</home/localuser/source/bifrost/pkg/hooks/codex_adversarial_test.go>), [FuzzCodexParse](</home/localuser/source/bifrost/pkg/hooks/codex_adversarial_test.go>).

Suggested fix: After decoding the expected root value, require EOF after whitespace. Preserve all valid whitespace and member-order variations. Keep the intentionally supported multi-document scenario/JSONL loader separate. Retain the design-required duplicate-key and depth checks; do not reject ordinary additional argument properties solely to fix this parser issue.

F32 · High · Current profiling instrumentation panics when the rule set is nil

Source: [tools.go:657](</home/localuser/source/bifrost/pkg/hooks/tools.go:657>), [tools.go:690](</home/localuser/source/bifrost/pkg/hooks/tools.go:690>).

Required behavior: No matching rules allows by default. Existing Set methods explicitly handle a nil rules receiver.

Verified behavior: An Engine.ListTools request with a valid catalog and Server.Rules:nil panics at s.prof. These profiling accesses appeared in external edits during this review. Proof: [CX-G15](</home/localuser/source/bifrost/pkg/hooks/codex_adversarial_test.go:316>).

Suggested fix: Resolve the optional profile pointer through a nil-safe accessor, or normalize absent rules at the public engine boundary. Preserve nil/default behavior consistently across listing and call APIs. Keep profiling disabled by default.

F16 · Medium · Fallback reports candidates that effective authorization rejects

Source: [resources.go:623](</home/localuser/source/bifrost/pkg/hooks/resources.go:623>).

Required behavior: Fallback candidates are limited to visible matches after filters, with no guessing.

Verified behavior: A resource covered by a denied template still becomes an exact candidate. A template-only URI denied by resources/filter still becomes a template candidate. Both create false ambiguity with the one readable server. Proof: [CX-15](</home/localuser/source/bifrost/pkg/hooks/testdata/scenarios/90-codex-adversarial.json:933>), [CX-16](</home/localuser/source/bifrost/pkg/hooks/testdata/scenarios/90-codex-adversarial.json:1006>).

Suggested fix: Authorize each candidate using the complete resource-read visibility predicate before reduction. Preserve exact-match precedence and refusal when an eligible catalog is unavailable. Use the same predicate to avoid disclosing denied manifest-only candidates.

F17 · Medium · Canonical URI refusal is not applied to returned identities

Source: [resources.go:850](</home/localuser/source/bifrost/pkg/hooks/resources.go:850>), [resources.go:875](</home/localuser/source/bifrost/pkg/hooks/resources.go:875>), [uris.go:126](</home/localuser/source/bifrost/pkg/hooks/uris.go:126>).

Required behavior: The design refuses dot segments, empty segments and percent-encoded unreserved characters to keep resource identity consistent.

Verified behavior: A directory response returns file:///public/%61, although directly reading that URI is refused. Substituted read content under file:///public/%62 also passes authorization. Proof: [CX-22](</home/localuser/source/bifrost/pkg/hooks/testdata/scenarios/90-codex-adversarial.json:1325>), [CX-23](</home/localuser/source/bifrost/pkg/hooks/testdata/scenarios/90-codex-adversarial.json:1373>).

Suggested fix: Validate every returned resource identity before containment or policy checks. Apply the same documented canonical rules as on inbound reads; drop invalid directory children and refuse or drop invalid substituted content consistently. Do not blanket-reject reserved percent escapes; see A01.

F18 · Medium · Changing one parameter turns an unrelated false schema into an accepting schema

Source: [tools.go:102](</home/localuser/source/bifrost/pkg/hooks/tools.go:102>), [tools.go:244](</home/localuser/source/bifrost/pkg/hooks/tools.go:244>).

Required behavior: The overlay changes only targeted properties. MCP allows JSON Schema keywords and boolean property schemas.

Verified behavior: Changing q.description changes an untouched forbidden:false property into forbidden:{}, reversing that property schema’s meaning. Proof: [CX-08](</home/localuser/source/bifrost/pkg/hooks/testdata/scenarios/90-codex-adversarial.json:447>).

Suggested fix: Preserve boolean property schemas as booleans when unchanged. If a rule annotates a boolean schema, wrap it in a semantically equivalent schema such as allOf:[false] plus the annotation. Never coerce false to an empty accepting schema or reject a valid untouched schema.

F19 · Medium · Empty skill files are omitted from digest recomputation

Source: [resources.go:408](</home/localuser/source/bifrost/pkg/hooks/resources.go:408>).

Required behavior: Rewriting a skill file recomputes digest and byte size for the bytes later returned.

Verified behavior: An empty text file rewritten to "filled" retains the empty-file digest and size zero in skills/get; resources/read returns "filled". Proof: [CX-28](</home/localuser/source/bifrost/pkg/hooks/testdata/scenarios/90-codex-adversarial.json:1768>).

Suggested fix: Represent text presence separately from its value, for example with a pointer or explicit content kind. Run rewrite rules on present empty text and update its digest and byte size. Keep binary and absent files distinguishable.

F20 · Medium · Nested SKILL.md files are mistaken for the enclosing skill identity

Source: [resources.go:422](</home/localuser/source/bifrost/pkg/hooks/resources.go:422>).

Required behavior: The Skills spec treats nested skill files as supporting content of the enclosing skill. Each skill has its own identity.

Verified behavior: Rewriting descriptions in a parent and child SKILL.md rejects the parent plan because the unchanged child name differs from the parent name. Proof: [CX-29](</home/localuser/source/bifrost/pkg/hooks/testdata/scenarios/90-codex-adversarial.json:1864>).

Suggested fix: Mirror frontmatter and enforce the parent name only for the entry’s exact root SKILL.md URI. Validate nested files against their own skill identity where applicable, while hashing them as supporting files of the parent.

F21 · Medium · Dynamic skill frontmatter is not mirrored after a static rewrite

Source: [resources.go:360](</home/localuser/source/bifrost/pkg/hooks/resources.go:360>).

Required behavior: Dynamic skills omit digests, but hosts still compare returned SKILL.md frontmatter with the entry.

Verified behavior: A resources:"dynamic" skill keeps description:"original" in skills/get while the same static rule returns description:"updated" on read. Proof: [CX-30](</home/localuser/source/bifrost/pkg/hooks/testdata/scenarios/90-codex-adversarial.json:1943>).

Suggested fix: Validate and mirror root frontmatter for dynamic skills as well as manifest-backed skills, keeping resources:"dynamic". If deterministic frontmatter cannot be produced, skip the rewrite consistently for entry and read rather than publish conflicting values.

F22 · Medium · Rewrite frontmatter parsing accepts invalid skill metadata

Source: [resources.go:463](</home/localuser/source/bifrost/pkg/hooks/resources.go:463>), [store.go:103](</home/localuser/source/bifrost/internal/e2e/upstream/skills/store.go:103>).

Required behavior: Skills require name and description. Rewritten entry frontmatter must equal the served SKILL.md frontmatter.

Verified behavior: The engine accepts a rewritten skill without description and a closing delimiter spelled ---not-a-fence. The upstream test server uses a separate, stricter parser, contrary to the catalogue’s single-parser claim. Proof: [CX-31](</home/localuser/source/bifrost/pkg/hooks/testdata/scenarios/90-codex-adversarial.json:2020>), [CX-G11](</home/localuser/source/bifrost/pkg/hooks/codex_adversarial_test.go:272>).

Suggested fix: Share one frontmatter parser and validator between fixture manifest generation and engine rewriting. Require a complete delimiter line and required typed fields. Preserve supported BOM/CRLF forms and arbitrary valid author metadata; do not curate metadata to a fixed subset. Reject invalid rewrite plans atomically.

F23 · Medium · Search definitions merge distinct case-sensitive identities

Source: [search.go:106](</home/localuser/source/bifrost/pkg/hooks/search.go:106>).

Required behavior: Names reverse exactly; the protocol says tool names should be treated as case-sensitive.

Verified behavior: Read and read remain distinct callable tools, but get_tool_definition lowercases both keys and returns read’s description for Read. Proof: [CX-39](</home/localuser/source/bifrost/pkg/hooks/testdata/scenarios/90-codex-adversarial.json:2467>).

Suggested fix: Index and resolve definitions by the exact exposed name, matching CallTool. Case-insensitive search ranking may remain a convenience, but exact definition lookup and dispatch must retain the selected identity.

F24 · Medium · Direct-route search returns names that direct execution cannot resolve

Source: [search.go:44](</home/localuser/source/bifrost/pkg/hooks/search.go:44>), [engine.go:247](</home/localuser/source/bifrost/pkg/hooks/engine.go:247>).

Required behavior: Search mode is available on direct and mux routes and uses the same execution pipeline.

Verified behavior: ToolSearch on a direct route returns a__lookup, but ExecuteTool on that route searches for the literal upstream name a__lookup and returns unknown tool. Proof: [CX-40](</home/localuser/source/bifrost/pkg/hooks/testdata/scenarios/90-codex-adversarial.json:2525>).

Suggested fix: Represent route topology and search mode separately. Prefix exposed names only on mux routes, and share one exposed-name formatter/resolver across search, definition lookup, listing and execution.

F25 · Medium · dropSchema does not mirror wildcard deletion

Source: [view.go:237](</home/localuser/source/bifrost/pkg/hooks/view.go:237>).

Required behavior: dropSchema removes the schema properties that drop removes from values, including required entries.

Verified behavior: dropSchema(schema,["*"]) leaves root properties a and b and required:[a,b] intact, while drop(value,["*"]) removes all fields. Proof: [CX-45](</home/localuser/source/bifrost/pkg/hooks/testdata/scenarios/90-codex-adversarial.json:2781>).

Suggested fix: Implement terminal wildcard deletion for object properties and matching requirements; preserve the appropriate empty-array semantics for array wildcards. Add paired data/schema tests. For reference-based schemas, support the requested transformation or return an explicit unsupported-edit error rather than silently leaving the schema unchanged.

F26 · Medium · Tool result rules cannot access the documented meta alias

Source: [server.go:368](</home/localuser/source/bifrost/pkg/hooks/server.go:368>).

Required behavior: tools/result exposes result.meta for upstream result metadata.

Verified behavior: The wire uses _meta, but resultVar never creates meta. Reading result.meta.trace therefore denies a valid upstream result with an evaluation error. Proof: [CX-65](</home/localuser/source/bifrost/pkg/hooks/testdata/scenarios/90-codex-adversarial.json:4343>).

Suggested fix: Expose a read-only result.meta alias of the original _meta map, with an empty map when absent. Keep _meta on the wire and preserve the existing metadata allowlist policy at its specified boundary.

F27 · Medium · Discovery does not describe the implemented 2026 protocol surface

Source: [server.go:145](</home/localuser/source/bifrost/pkg/hooks/server.go:145>), [server.go:159](</home/localuser/source/bifrost/pkg/hooks/server.go:159>).

Required behavior: DiscoverResult requires supportedVersions. Clients call skills methods only after observing the Skills extension declaration.

Verified behavior: Discover omits supportedVersions and never declares the Skills extension while skills/list succeeds. It also places server identity at the legacy top level rather than the spec’s optional _meta key. Proof: [CX-66](</home/localuser/source/bifrost/pkg/hooks/testdata/scenarios/90-codex-adversarial.json:4404>), [CX-67](</home/localuser/source/bifrost/pkg/hooks/testdata/scenarios/90-codex-adversarial.json:4442>).

Suggested fix: Emit supportedVersions:["2026-07-28"] and derive extension capabilities from the methods actually exposed on the route. Declare directoryRead only where supported. Put display identity under _meta["io.modelcontextprotocol/serverInfo"] when enabled; that identity field is SHOULD/optional, whereas supportedVersions is required.

F28 · Medium · The engine does not implement property-level copy-on-write

Source: [tools.go:57](</home/localuser/source/bifrost/pkg/hooks/tools.go:57>), [tools.go:454](</home/localuser/source/bifrost/pkg/hooks/tools.go:454>).

Required behavior: The item view points at the cached upstream object; changing one property preserves untouched sibling pointers and avoids whole-schema copying.

Verified behavior: The engine marshals and unmarshals the whole tool before evaluation, then again for changed output. Updating q.description loses pointer identity for an untouched tenant schema. Isolated View tests do not exercise this path. Proof: [CX-G04](</home/localuser/source/bifrost/pkg/hooks/codex_adversarial_test.go:102>).

Suggested fix: Build views over cached decoded objects or a lazy typed adapter, and apply path overlays directly. Materialize only changed paths at response time. Retain the existing concurrency guarantees and test pointer identity through the actual ListTools pipeline.

F29 · Medium · An audit-only patch satisfies hidden-required validation without supplying a value

Source: [tools.go:732](</home/localuser/source/bifrost/pkg/hooks/tools.go:732>), [tools.go:425](</home/localuser/source/bifrost/pkg/hooks/tools.go:425>).

Required behavior: Hiding a required parameter needs a supplying pin/default or an effective call patch. Audit rules never change arguments.

Verified behavior: callFilled includes audit rules. A required tenant hidden without a default or pin is forwarded absent because an audit-only rule syntactically names args.tenant. Proof: [CX-G10](</home/localuser/source/bifrost/pkg/hooks/codex_adversarial_test.go:260>).

Suggested fix: Exclude audit rules from static supplying-rule analysis. More generally, verify after actual call evaluation that each hidden required upstream parameter has a supplied value; merely finding a conditional assignment in the policy is insufficient.

F30 · Medium · The scenario runner drops search continuation fields

Source: [scenario.go:433](</home/localuser/source/bifrost/pkg/hooks/scenario.go:433>), [search.go:123](</home/localuser/source/bifrost/pkg/hooks/search.go:123>).

Required behavior: execute_tool uses the complete tools/call pipeline, including continuation parameters.

Verified behavior: The runner decodes only name and arguments, discarding requestState and inputResponses. The equivalent direct Engine.ExecuteTool API test passes. Proof: [CX-53](</home/localuser/source/bifrost/pkg/hooks/testdata/scenarios/90-codex-adversarial.json:3462>), [CX-G03](</home/localuser/source/bifrost/pkg/hooks/codex_adversarial_test.go:88>).

Suggested fix: Decode continuation fields in the scenario step and invoke Engine.ExecuteTool with them. Keep CX-53 as a runner regression test. This is a harness defect, not evidence that Engine.ExecuteTool drops state.

F31 · Medium · Scenario equality assertions can certify the wrong large integer

Source: [scenario.go:569](</home/localuser/source/bifrost/pkg/hooks/scenario.go:569>), [scenario.go:613](</home/localuser/source/bifrost/pkg/hooks/scenario.go:613>).

Required behavior: Tests claiming exact numeric preservation must distinguish mathematically different JSON numbers.

Verified behavior: An expectation equals:9007199254740992 passes against an observed json.Number("9007199254740993") because both sides are normalized through float64. Proof: [CX-G09](</home/localuser/source/bifrost/pkg/hooks/codex_adversarial_test.go:249>).

Suggested fix: Decode expected values with UseNumber and compare recursively using an exact semantic comparator independent of the engine’s policy comparator. Accept equivalent number spellings, preserve array order, ignore object key order, and keep JSON strings/booleans distinct from numbers.

F33 · Medium · Valid upstream input requests lose answers when requestState is omitted

Source: [engine.go:360](</home/localuser/source/bifrost/pkg/hooks/engine.go:360>), [resources.go:157](</home/localuser/source/bifrost/pkg/hooks/resources.go:157>), [resources.go:804](</home/localuser/source/bifrost/pkg/hooks/resources.go:804>).

Required behavior: InputRequiredResult may contain inputRequests without requestState. The state field is optional in MCP 2026-07-28.

Verified behavior: For tools, prompts and resources, the gateway wraps a valid challenge but forwards inputResponses only if the upstream state string is nonempty. Stateless upstream prompts therefore lose the answers. Proof: [CX-48](</home/localuser/source/bifrost/pkg/hooks/testdata/scenarios/90-codex-adversarial.json:3055>), [CX-49](</home/localuser/source/bifrost/pkg/hooks/testdata/scenarios/90-codex-adversarial.json:3146>), [CX-50](</home/localuser/source/bifrost/pkg/hooks/testdata/scenarios/90-codex-adversarial.json:3233>).

Suggested fix: Track a validated upstream continuation independently of the optional upstream state value. Forward its validated responses even when requestState is absent; preserve any supplied opaque state exactly. This protocol fix is independent of the planned KV storage.

F34 · Medium · Valid state-only input-required replies bypass wrapping

Source: [resources.go:189](</home/localuser/source/bifrost/pkg/hooks/resources.go:189>), [resources.go:829](</home/localuser/source/bifrost/pkg/hooks/resources.go:829>).

Required behavior: MCP explicitly permits input_required containing requestState without inputRequests, for example load shedding.

Verified behavior: Prompt and resource reply handlers recognize input_required only when inputRequests is a map. A state-only reply returns the raw upstream token, which the next request rejects as invalid gateway state. Proof: [CX-51](</home/localuser/source/bifrost/pkg/hooks/testdata/scenarios/90-codex-adversarial.json:3321>), [CX-52](</home/localuser/source/bifrost/pkg/hooks/testdata/scenarios/90-codex-adversarial.json:3391>).

Suggested fix: Dispatch by resultType first. Handle both permitted forms: requests with optional state, and state-only. Wrap any upstream state for routing/binding without requiring an elicitation capability when no user input is requested. Do not fabricate input requests.

F36 · Medium · Equivalent integral JSON spellings produce different policy outcomes

Source: [engine.go:482](</home/localuser/source/bifrost/pkg/hooks/engine.go:482>).

Required behavior: The input schema declares an integer. JSON Schema integer semantics accept numeric values with zero fractional part, independently of decimal or exponent spelling. The adapter itself promises int64 when integral.

Verified behavior: With a call rule args.n + 1, the input 1 forwards n:2. The valid equivalent spellings 1.0, 1e0, 1E+0 and 0.1e1 all produce an isError result with a CEL no-such-overload error. Int64 parses the token spelling, and the fallback turns the equivalent values into doubles. Proof: [CX-G17](</home/localuser/source/bifrost/pkg/hooks/codex_adversarial_test.go:353>).

Suggested fix: Normalize numeric values for CEL from exact mathematical value and the declared schema, rather than lexical spelling. An in-range integral value under an integer schema must have the same CEL type for all valid JSON representations. Keep raw argument values separate for forwarding. Preserve fractional numbers where permitted and do not coerce quoted numbers or booleans.

F35 · Low · Resource, template and skill order differs from the agreed name order

Source: [resources.go:521](</home/localuser/source/bifrost/pkg/hooks/resources.go:521>), [resources.go:564](</home/localuser/source/bifrost/pkg/hooks/resources.go:564>), [resources.go:285](</home/localuser/source/bifrost/pkg/hooks/resources.go:285>).

Required behavior: The design decisions require slug a-z followed by name a-z.

Verified behavior: Resources and templates sort by URI identity; skills sort by URI ancestry. A name alpha with URI z sorts after zebra with URI a. Proof: [CX-41](</home/localuser/source/bifrost/pkg/hooks/testdata/scenarios/90-codex-adversarial.json:2585>), [CX-42](</home/localuser/source/bifrost/pkg/hooks/testdata/scenarios/90-codex-adversarial.json:2627>), [CX-43](</home/localuser/source/bifrost/pkg/hooks/testdata/scenarios/90-codex-adversarial.json:2669>).

Suggested fix: Sort each slug’s final visible records by exposed name, with URI as a stable tiebreaker. If URI order is preferred for some kinds, revise the design and catalogue explicitly rather than claiming the current global name-order contract is proven.

Design ambiguities and deferred decisions

A01 — Reserved URI escapes. [CX-19](</home/localuser/source/bifrost/pkg/hooks/testdata/scenarios/90-codex-adversarial.json:1157>) expects file:///private%2Fsecret to be denied under a raw /private/ prefix rule. The current design explicitly refuses encoded unreserved characters, but does not say that encoded slash is a separator. RFC URI semantics do not make %2F universally equivalent to /. The engine forwards the URI; this failure is classified as a design ambiguity, not a confirmed authorization bypass. Suggested resolution: define scheme-aware canonical identity, keep reserved escapes opaque unless the upstream’s scheme contract says otherwise, and reject only ambiguous spellings covered by that contract. Make policy matching and upstream routing use that same identity. Do not globally decode reserved characters or reject all percent escapes.

Audit budget precedence needs an explicit decision. F13 proves the current implementation denies because of audit work. The audit invariant says outcomes do not change; the global budget and complete-log requirements pull in different directions. The suggested separate audit budget preserves the audit invariant and must explicitly report truncated audit execution.

Hidden required parameters are described in two ways. The hook reference requires pin/default; RH-REV-15 and existing scenarios also permit tools/call to supply the value. The implementation chose the latter. This is reasonable when the actual call produces the value. F29 addresses the narrower bug where an audit-only assignment is counted even though it cannot supply anything.

Unknown argument keys and arbitrary JSON schemas should not be tightened as a side effect of this review. The design restricts renamed/hidden identities but does not specify full JSON Schema validation of all extra properties at runtime. The engine currently forwards keys that were never declared. The fixes above require tracking known hidden/added identities, not rejecting every unknown key or valid schema composition. The existing explicit rejection of unsupported parameter transformations should remain scoped to the requested edit, with a clear error.

D01 — Current state does not fully bind method and entitlement identity. [CX-46](</home/localuser/source/bifrost/pkg/hooks/testdata/scenarios/90-codex-adversarial.json:2846>) reuses a resources/read challenge for resources/directory/read because [resources.go:779](</home/localuser/source/bifrost/pkg/hooks/resources.go:779>) hardcodes resources/read. [CX-G01](</home/localuser/source/bifrost/pkg/hooks/codex_adversarial_test.go:55>) reuses a token across two same-slug entitlements in different organizations because [engine.go:539](</home/localuser/source/bifrost/pkg/hooks/engine.go:539>) binds the slug instead of the entitlement ID. These are verified current-code failures, deferred to the KV redesign. Retain method, immutable entitlement ID, authenticated principal scope, target, policy generation and argument identity in the KV lookup/binding contract.

D02 — The gateway answer completing the current round is not carried into upstream continuation state. [CX-47](</home/localuser/source/bifrost/pkg/hooks/testdata/scenarios/90-codex-adversarial.json:2925>) returns a second gateway confirmation after the upstream asks for input. [server.go:465](</home/localuser/source/bifrost/pkg/hooks/server.go:465>) copies previous stored answers but omits the newly validated answer. Deferred to the KV redesign: persist the validated accumulated answers before transitioning into an upstream round. Same-argument replay within five minutes is an accepted current-design tradeoff, and [CX-54](</home/localuser/source/bifrost/pkg/hooks/testdata/scenarios/90-codex-adversarial.json:3545>) verifies it; replay alone is not reported as a defect.

What the existing tests do not establish

- ADV-19: Tests large-number forwarding, not distinct-value enum/pin/range behavior. Scenario equality itself rounds numbers (F31). The new exact policy tests fail. Source: [07-search-fresh-limits.json:80](</home/localuser/source/bifrost/pkg/hooks/testdata/scenarios/07-search-fresh-limits.json:80>).

- ADV-22 generation test: Uses a token ending in .x, not a valid signature, so it proves invalid-token rejection rather than generation binding. CX-G02 uses an actually issued token and confirms generation rejection works. Source: [08-gaps.json:22](</home/localuser/source/bifrost/pkg/hooks/testdata/scenarios/08-gaps.json:22>).

- ADV-22 method binding: Tests tool-to-resource/target substitution, but not read-to-directory substitution. CX-46 demonstrates the omitted case; deferred to KV work. Source: [08-gaps.json:13](</home/localuser/source/bifrost/pkg/hooks/testdata/scenarios/08-gaps.json:13>).

- ADV-39: Exercises missing-field audit errors, not aggregate audit time. CX-G06 exceeds a real deadline and changes the outcome. Source: [08-gaps.json:37](</home/localuser/source/bifrost/pkg/hooks/testdata/scenarios/08-gaps.json:37>).

- ADV-31 and RH-SKL-09: Only inspect skills/get after a rejected rewrite. They do not read either the root file or the supporting file after rejection. CX-26 and CX-27 do, and fail. Source: [06-resources-skills-mux.json:2027](</home/localuser/source/bifrost/pkg/hooks/testdata/scenarios/06-resources-skills-mux.json:2027>).

- ADV-47: Checks the listing-side decision to skip a dynamic rewrite, not the later file bytes. CX-24 and CX-25 expose the mismatch. Source: [06-resources-skills-mux.json:2208](</home/localuser/source/bifrost/pkg/hooks/testdata/scenarios/06-resources-skills-mux.json:2208>).

- ADV-46: Exercises true, not false. Converting true into {} preserves acceptance; converting false into {} does not. CX-08 tests the missing distinction. Source: [07-search-fresh-limits.json:109](</home/localuser/source/bifrost/pkg/hooks/testdata/scenarios/07-search-fresh-limits.json:109>).

- ADV-45: Uses aliased native Go maps, not JSON Schema $ref, $defs or recursive references described in the original adversarial case. It proves map immutability only. Reference-aware dropSchema behavior remains unproven. Source: [hooks_test.go:150](</home/localuser/source/bifrost/pkg/hooks/hooks_test.go:150>).

- RH-COW-02/05: Tests View in isolation and counts changed final tool pointers. It does not prove the actual pipeline avoids full serialization or preserves untouched schema siblings. CX-G04 fails through ListTools. Source: [hooks_test.go:59](</home/localuser/source/bifrost/pkg/hooks/hooks_test.go:59>).

- RH-CALL-08: Changes now, not the rule set. This proves reevaluation of a time-sensitive rule, not a policy replacement. Source: [03-reversal-calls.json:109](</home/localuser/source/bifrost/pkg/hooks/testdata/scenarios/03-reversal-calls.json:109>).

- RH-SRV-07: Starts with a fixed "version two" instruction and performs one discover. It does not perform two fetches or demonstrate a live upstream change. Live fetching is integration work. Source: [05-server-prompts.json:39](</home/localuser/source/bifrost/pkg/hooks/testdata/scenarios/05-server-prompts.json:39>).

- RH-PRF-03 and real-call policy tests: Unchanged-tool pointer identity does not validate modified schemas. TestRealCallExamplesUnderPolicy accepts every nonempty outcome category, so it is a crash/smoke test, not a per-call authorization oracle. Source: [hooks_test.go:304](</home/localuser/source/bifrost/pkg/hooks/hooks_test.go:304>).

- ADV-28: Existing concurrency checks compare names and one metadata key. CX-G05 additionally checks distinct entitlement pins, renamed argument reversal and the entire original catalog. The stronger test passes. Source: [hooks_test.go:322](</home/localuser/source/bifrost/pkg/hooks/hooks_test.go:322>).

- RH-SRV-10: Checks the progress configuration value, not delivery on the originating stream. Stream routing is explicitly deferred to gateway integration. Source: [08-gaps.json:2](</home/localuser/source/bifrost/pkg/hooks/testdata/scenarios/08-gaps.json:2>).

- ADV-48: The claim that one parser handles manifest generation and rewriting is false: resources.go and upstream/skills/store.go contain different parsers with different validation and BOM/CRLF behavior. Source: [test-cases.md:287](</home/localuser/source/bifrost/docs/rules/test-cases.md:287>).

All new scenario verdicts are listed below. Expectations remain design-based; assertions were not inverted to make the implementation pass. Fifty-five failures show current engine deviations, including two cases deferred to KV work; one is a runner defect and one is a URI design ambiguity. No remaining failure is attributed to an incorrectly constructed scenario. “Pass” is limited to the assertions in that scenario.

| Scenario | Result | Classification | Finding |
| --- | --- | --- | --- |
| [CX-01](</home/localuser/source/bifrost/pkg/hooks/testdata/scenarios/90-codex-adversarial.json:3>) | fail | engine wrong | F01 |
| [CX-02](</home/localuser/source/bifrost/pkg/hooks/testdata/scenarios/90-codex-adversarial.json:63>) | fail | engine wrong | F01 |
| [CX-03](</home/localuser/source/bifrost/pkg/hooks/testdata/scenarios/90-codex-adversarial.json:123>) | fail | engine wrong | F01 |
| [CX-04](</home/localuser/source/bifrost/pkg/hooks/testdata/scenarios/90-codex-adversarial.json:183>) | fail | engine wrong | F01 |
| [CX-05](</home/localuser/source/bifrost/pkg/hooks/testdata/scenarios/90-codex-adversarial.json:245>) | fail | engine wrong | F02 |
| [CX-06](</home/localuser/source/bifrost/pkg/hooks/testdata/scenarios/90-codex-adversarial.json:315>) | fail | engine wrong | F02 |
| [CX-07](</home/localuser/source/bifrost/pkg/hooks/testdata/scenarios/90-codex-adversarial.json:384>) | fail | engine wrong | F03 |
| [CX-08](</home/localuser/source/bifrost/pkg/hooks/testdata/scenarios/90-codex-adversarial.json:447>) | fail | engine wrong | F18 |
| [CX-09](</home/localuser/source/bifrost/pkg/hooks/testdata/scenarios/90-codex-adversarial.json:498>) | fail | engine wrong | F04 |
| [CX-10](</home/localuser/source/bifrost/pkg/hooks/testdata/scenarios/90-codex-adversarial.json:569>) | fail | engine wrong | F04 |
| [CX-11](</home/localuser/source/bifrost/pkg/hooks/testdata/scenarios/90-codex-adversarial.json:653>) | fail | engine wrong | F04 |
| [CX-12](</home/localuser/source/bifrost/pkg/hooks/testdata/scenarios/90-codex-adversarial.json:743>) | fail | engine wrong | F05 |
| [CX-13](</home/localuser/source/bifrost/pkg/hooks/testdata/scenarios/90-codex-adversarial.json:807>) | fail | engine wrong | F05 |
| [CX-14](</home/localuser/source/bifrost/pkg/hooks/testdata/scenarios/90-codex-adversarial.json:871>) | fail | engine wrong | F05 |
| [CX-15](</home/localuser/source/bifrost/pkg/hooks/testdata/scenarios/90-codex-adversarial.json:933>) | fail | engine wrong | F16 |
| [CX-16](</home/localuser/source/bifrost/pkg/hooks/testdata/scenarios/90-codex-adversarial.json:1006>) | fail | engine wrong | F16 |
| [CX-17](</home/localuser/source/bifrost/pkg/hooks/testdata/scenarios/90-codex-adversarial.json:1073>) | fail | engine wrong | F06 |
| [CX-18](</home/localuser/source/bifrost/pkg/hooks/testdata/scenarios/90-codex-adversarial.json:1115>) | fail | engine wrong | F06 |
| [CX-19](</home/localuser/source/bifrost/pkg/hooks/testdata/scenarios/90-codex-adversarial.json:1157>) | fail | design ambiguous | A01 |
| [CX-20](</home/localuser/source/bifrost/pkg/hooks/testdata/scenarios/90-codex-adversarial.json:1207>) | fail | engine wrong | F07 |
| [CX-21](</home/localuser/source/bifrost/pkg/hooks/testdata/scenarios/90-codex-adversarial.json:1264>) | fail | engine wrong | F07 |
| [CX-22](</home/localuser/source/bifrost/pkg/hooks/testdata/scenarios/90-codex-adversarial.json:1325>) | fail | engine wrong | F17 |
| [CX-23](</home/localuser/source/bifrost/pkg/hooks/testdata/scenarios/90-codex-adversarial.json:1373>) | fail | engine wrong | F17 |
| [CX-24](</home/localuser/source/bifrost/pkg/hooks/testdata/scenarios/90-codex-adversarial.json:1421>) | fail | engine wrong | F14 |
| [CX-25](</home/localuser/source/bifrost/pkg/hooks/testdata/scenarios/90-codex-adversarial.json:1507>) | fail | engine wrong | F14 |
| [CX-26](</home/localuser/source/bifrost/pkg/hooks/testdata/scenarios/90-codex-adversarial.json:1593>) | fail | engine wrong | F14 |
| [CX-27](</home/localuser/source/bifrost/pkg/hooks/testdata/scenarios/90-codex-adversarial.json:1676>) | fail | engine wrong | F14 |
| [CX-28](</home/localuser/source/bifrost/pkg/hooks/testdata/scenarios/90-codex-adversarial.json:1768>) | fail | engine wrong | F19 |
| [CX-29](</home/localuser/source/bifrost/pkg/hooks/testdata/scenarios/90-codex-adversarial.json:1864>) | fail | engine wrong | F20 |
| [CX-30](</home/localuser/source/bifrost/pkg/hooks/testdata/scenarios/90-codex-adversarial.json:1943>) | fail | engine wrong | F21 |
| [CX-31](</home/localuser/source/bifrost/pkg/hooks/testdata/scenarios/90-codex-adversarial.json:2020>) | fail | engine wrong | F22 |
| [CX-32](</home/localuser/source/bifrost/pkg/hooks/testdata/scenarios/90-codex-adversarial.json:2086>) | fail | engine wrong | F09 |
| [CX-33](</home/localuser/source/bifrost/pkg/hooks/testdata/scenarios/90-codex-adversarial.json:2142>) | fail | engine wrong | F09 |
| [CX-34](</home/localuser/source/bifrost/pkg/hooks/testdata/scenarios/90-codex-adversarial.json:2193>) | fail | engine wrong | F10 |
| [CX-35](</home/localuser/source/bifrost/pkg/hooks/testdata/scenarios/90-codex-adversarial.json:2239>) | fail | engine wrong | F10 |
| [CX-36](</home/localuser/source/bifrost/pkg/hooks/testdata/scenarios/90-codex-adversarial.json:2292>) | fail | engine wrong | F10 |
| [CX-37](</home/localuser/source/bifrost/pkg/hooks/testdata/scenarios/90-codex-adversarial.json:2335>) | fail | engine wrong | F10 |
| [CX-38](</home/localuser/source/bifrost/pkg/hooks/testdata/scenarios/90-codex-adversarial.json:2393>) | fail | engine wrong | F10 |
| [CX-39](</home/localuser/source/bifrost/pkg/hooks/testdata/scenarios/90-codex-adversarial.json:2467>) | fail | engine wrong | F23 |
| [CX-40](</home/localuser/source/bifrost/pkg/hooks/testdata/scenarios/90-codex-adversarial.json:2525>) | fail | engine wrong | F24 |
| [CX-41](</home/localuser/source/bifrost/pkg/hooks/testdata/scenarios/90-codex-adversarial.json:2585>) | fail | engine wrong | F35 |
| [CX-42](</home/localuser/source/bifrost/pkg/hooks/testdata/scenarios/90-codex-adversarial.json:2627>) | fail | engine wrong | F35 |
| [CX-43](</home/localuser/source/bifrost/pkg/hooks/testdata/scenarios/90-codex-adversarial.json:2669>) | fail | engine wrong | F35 |
| [CX-44](</home/localuser/source/bifrost/pkg/hooks/testdata/scenarios/90-codex-adversarial.json:2719>) | fail | engine wrong | F06 |
| [CX-45](</home/localuser/source/bifrost/pkg/hooks/testdata/scenarios/90-codex-adversarial.json:2781>) | fail | engine wrong | F25 |
| [CX-46](</home/localuser/source/bifrost/pkg/hooks/testdata/scenarios/90-codex-adversarial.json:2846>) | fail | engine wrong in current code; deferred to KV redesign | D01 |
| [CX-47](</home/localuser/source/bifrost/pkg/hooks/testdata/scenarios/90-codex-adversarial.json:2925>) | fail | engine wrong in current code; deferred to KV redesign | D02 |
| [CX-48](</home/localuser/source/bifrost/pkg/hooks/testdata/scenarios/90-codex-adversarial.json:3055>) | fail | engine wrong | F33 |
| [CX-49](</home/localuser/source/bifrost/pkg/hooks/testdata/scenarios/90-codex-adversarial.json:3146>) | fail | engine wrong | F33 |
| [CX-50](</home/localuser/source/bifrost/pkg/hooks/testdata/scenarios/90-codex-adversarial.json:3233>) | fail | engine wrong | F33 |
| [CX-51](</home/localuser/source/bifrost/pkg/hooks/testdata/scenarios/90-codex-adversarial.json:3321>) | fail | engine wrong | F34 |
| [CX-52](</home/localuser/source/bifrost/pkg/hooks/testdata/scenarios/90-codex-adversarial.json:3391>) | fail | engine wrong | F34 |
| [CX-53](</home/localuser/source/bifrost/pkg/hooks/testdata/scenarios/90-codex-adversarial.json:3462>) | fail | scenario harness wrong; API passes | F30 |
| [CX-54](</home/localuser/source/bifrost/pkg/hooks/testdata/scenarios/90-codex-adversarial.json:3545>) | pass | passes | — |
| [CX-55](</home/localuser/source/bifrost/pkg/hooks/testdata/scenarios/90-codex-adversarial.json:3647>) | pass | passes | — |
| [CX-56](</home/localuser/source/bifrost/pkg/hooks/testdata/scenarios/90-codex-adversarial.json:3730>) | pass | passes | — |
| [CX-57](</home/localuser/source/bifrost/pkg/hooks/testdata/scenarios/90-codex-adversarial.json:3825>) | pass | passes | — |
| [CX-58](</home/localuser/source/bifrost/pkg/hooks/testdata/scenarios/90-codex-adversarial.json:3897>) | pass | passes | — |
| [CX-59](</home/localuser/source/bifrost/pkg/hooks/testdata/scenarios/90-codex-adversarial.json:3970>) | pass | passes | — |
| [CX-60](</home/localuser/source/bifrost/pkg/hooks/testdata/scenarios/90-codex-adversarial.json:4028>) | pass | passes | — |
| [CX-61](</home/localuser/source/bifrost/pkg/hooks/testdata/scenarios/90-codex-adversarial.json:4086>) | pass | passes | — |
| [CX-62](</home/localuser/source/bifrost/pkg/hooks/testdata/scenarios/90-codex-adversarial.json:4149>) | pass | passes | — |
| [CX-63](</home/localuser/source/bifrost/pkg/hooks/testdata/scenarios/90-codex-adversarial.json:4212>) | pass | passes | — |
| [CX-64](</home/localuser/source/bifrost/pkg/hooks/testdata/scenarios/90-codex-adversarial.json:4282>) | fail | engine wrong | F08 |
| [CX-65](</home/localuser/source/bifrost/pkg/hooks/testdata/scenarios/90-codex-adversarial.json:4343>) | fail | engine wrong | F26 |
| [CX-66](</home/localuser/source/bifrost/pkg/hooks/testdata/scenarios/90-codex-adversarial.json:4404>) | fail | engine wrong | F27 |
| [CX-67](</home/localuser/source/bifrost/pkg/hooks/testdata/scenarios/90-codex-adversarial.json:4442>) | fail | engine wrong | F27 |
| [CX-68](</home/localuser/source/bifrost/pkg/hooks/testdata/scenarios/90-codex-adversarial.json:4503>) | pass | passes | — |
| [CX-69](</home/localuser/source/bifrost/pkg/hooks/testdata/scenarios/90-codex-adversarial.json:4578>) | pass | passes | — |
| [CX-70](</home/localuser/source/bifrost/pkg/hooks/testdata/scenarios/90-codex-adversarial.json:4695>) | pass | passes | — |


Reproduce the review locally with these commands. They deliberately exit nonzero while the confirmed defects remain:

```bash
go build -o bin/hookctl ./cmd/hookctl
./bin/hookctl run -summary pkg/hooks/testdata/scenarios/*.json
./bin/hookctl run -summary -only CX-01 pkg/hooks/testdata/scenarios/90-codex-adversarial.json
go test ./pkg/hooks/ -race -count=1
go test ./pkg/hooks/ -race -run "^TestCodex" -count=1
go test ./pkg/hooks/ -run "^$" -fuzz "^FuzzCodexURI$" -fuzztime=5s -parallel=2
```


To repeat only the original CLI scenarios, pass pkg/hooks/testdata/scenarios/0*.json. To reproduce a particular Go finding, use its TestCodex function name in [the adversarial Go test file](</home/localuser/source/bifrost/pkg/hooks/codex_adversarial_test.go>). The suite includes the real 1,481-definition corpus and real call examples with their original definitions, read only. Two new real-call scenarios rename and reverse arguments for Bash and Read; the command strings are data and are never executed.

Review version: branch rules-hooks, HEAD 1cf47283d48619c93c8f37f932564c16f4af512d, with uncommitted files. External profiling changes modified rule.go, tools.go and eval.go and added profile.go/bench_test.go during the review. The CLI was rebuilt and findings rerun afterwards. Hashes of the reviewed implementation and tests are in the evidence JSON. The unchanged real-fixture hashes were checked against the starting snapshot. Findings refer to this reviewed version; later edits need the same regressions rerun.
