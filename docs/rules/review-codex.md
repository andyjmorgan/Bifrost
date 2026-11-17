# Adversarial review of ba03834

The latest fixes still leave policy bypasses. Seven new URI spellings pass all four denial checks tested. The query and trailing-slash protection has gaps. A read can also return a skill entry that a rule dropped. Confirmation hashes merge distinct exact numbers. I found nine issues: three critical under the stated conditions, one high, four medium and one low. The new engine tests are in `pkg/hooks/review_codex_test.go`. No engine code, existing tests, fixtures or existing documentation changed. No commits, pushes or Git state changes were made.

## C1. Further URL spellings pass every URI denial check

Severity: critical when the server resolves resource names with WHATWG URL normalization. The caller must hold the entitlement, and its separate resource selection must permit the request. This does not grant another entitlement.

These are new cases beyond findings 3 and V1. Each row gives the protected URI and a request URI that reached `Forward`:

| Protected URI | Accepted alternate spelling |
| --- | --- |
| `https://[::1]/private/x` | `https://[0:0:0:0:0:0:0:1]/private/x` |
| `https://xn--bcher-kva.example/private/x` | `https://bücher.example/private/x` |
| `https://wiki.example/private/x` | `https://%77iki.example/private/x` |
| `https://127.0.0.1/private/x` | `https://2130706433/private/x` |
| `https://wiki.example/private/x` | `https://wiki.example:0443/private/x` |
| `https://wiki.example/private/x` | `https://ｗｉｋｉ.example/private/x` |
| `https://wiki.example/caf%C3%A9/x` | `https://wiki.example/café/x` |

For each row, the test creates four independent policies. Below are the exact policies for the `wiki.example/private/x` rows. The other rows substitute their protected URI or its parent prefix:

```text
resources/list.post
  when: resource.uri.startsWith('https://wiki.example/private/')
  then: decision: 'deny'

resources/list.post
  when: resource.uri == 'https://wiki.example/private/x'
  then: decision: 'deny'

resources/list.post
  when: resource.?name.orValue('') == 'secret'
  then: decision: 'deny'
  catalog: resource URI https://wiki.example/private/x, name secret

resources/templates/list.post
  when: true
  then: decision: 'deny'
  catalog: template https://wiki.example/private/{id}, name secret
```

Request: `resources/read`, `{"uri":"<alternate spelling>"}`. On the mux, prepend `a+`. All 56 alternate requests forwarded unchanged. All 56 protected-URI controls were refused.

I ran Node's `new URL(uri).href` locally. Both strings in each row produced exactly the same URL. No DNS or HTTP request was made. A server using that lookup would return the protected resource; that server behavior is conditional, not an end-to-end result.

`nonCanonical` in `pkg/hooks/uris.go:141` checks host case and a few textual forms. It does not establish a unique URL spelling. The response to V1 and the overview's canonical-form section promise that it does.

Test: `TestReviewCodexGapURIAliases`. Ran with `HOOKS_KNOWN_GAPS=1` and `-race`; all 28 subtests failed as expected. Reserved escapes such as `%2F` are not part of this finding.

## C2. Query and slash protection has missing cases

Severity: critical when the server treats the tested variant as the protected resource. These cases attack the new `bare` and `checkURI` logic.

Rule for the first three cases:

```text
resources/list.post
when: resource.?name.orValue('') == 'secret'
then: decision: 'deny'
```

| Catalog | Request |
| --- | --- |
| `https://wiki.example`, name `secret` | read `https://wiki.example/` |
| `docs://h/private`, name `secret`; `docs://h/private?view=raw`, name `alias` | read `docs://h/private?view=raw` |
| `docs://h/private`, name `secret`; `docs://h/private/`, name `alias` | read `docs://h/private/` |

All three requests forwarded. The hidden base URI was refused in each control. A read of `docs://h/public` returning an entry for any of these aliases also passed that entry, including its `SECRET` text.

The authority-root slash survives `bare`: it strips a slash only when more than one slash follows `://`. For the two listed aliases, `checkURI` returns the visible listed item before checking its hidden base URI.

A fourth case uses a dynamic skill at `skill://demo/SKILL.md` and this rule:

```text
skills/list.post
when: true
then: decision: 'deny'
```

Both `resources/read` and `resources/directory/read` of `skill://demo?view=raw` forwarded. The slash form, `skill://demo/?view=raw`, was refused. `deniedUnder` compares the unstripped URI with the folder root, so the query prevents the root match. I tested forwarding, not a real directory server's response.

The response to V1 says a hidden listed resource covers an added query or trailing slash. The overview also says a denied skill folder is unreadable. The first three cases contradict the former; the fourth contradicts the latter when the server ignores the query.

Tests: `TestReviewCodexGapHiddenVariants` and `TestReviewCodexGapSkillRootQuery`. Ran with the gap flag and `-race`. Server equivalence for the custom `docs:` and `skill:` forms is a stated condition, not an observed server result.

## C3. A dropped skill entry comes back as the retained entry

Severity: critical under the reproduced conditions. A content-filtering policy is bypassed when a skill read returns two entries with the same URI. This is an engine finding; the gateway currently does not populate its skill catalog.

Catalog: dynamic skill `skill://demo/SKILL.md`. Rule:

```text
resources/read.post.result
when: true
then: response.result.contents:
  response.result.contents.filter(c, !c.text.contains('SECRET'))
```

Request: read `skill://demo/x.md`. Server response:

```json
{"contents":[
  {"uri":"skill://demo/x.md","text":"public"},
  {"uri":"skill://demo/x.md","text":"SECRET"}
]}
```

The client received one entry containing `SECRET`. The rule had retained only `public`. `UpstreamReadReply` stores protected entries in `fixed[uri]`, so the last entry overwrites the first. After filtering, it restores that last entry into the surviving slot (`resources.go:1061`, `resources.go:1082`).

The result-hook documentation explicitly allows a rule to drop a protected skill entry. Preserving bytes must not restore a different entry that the rule removed. Either retaining the public entry or refusing ambiguous duplicate entries would preserve that boundary.

Test: `TestReviewCodexGapDroppedSkillEntryReturns`. Ran with the gap flag and `-race`. The secret appeared in the engine's final response; no upstream parser assumption is needed.

## C4. One confirmation authorizes a different exact number

Severity: high. The binding is weaker than the arguments forwarded to the server. A server that distinguishes exact JSON numbers can receive a value the user did not confirm.

Catalog: tool `t`, input schema `{"type":"object"}`. Rule:

```text
tools/call.pre
when: true
then: confirm: elicit('sure', 'Proceed?')
```

Call `t` with `{"nested":{"values":[9007199254740992.0]}}`. Answer `bifrost.sure` with `action: accept`, `content: {"confirm":true}`, and the issued state key. Change the arguments to `{"nested":{"values":[9007199254740993.0]}}`.

The second call forwarded without another question. Its outgoing JSON retained `9007199254740993.0`. The same happened for these distinct pairs:

```text
0.100000000000000001  -> 0.100000000000000002
1e-400              -> 2e-400
```

`canonical` in `state.go:133` converts non-integer spellings through `float64`. Both the initial arguments hash and the question binding collide after rounding or underflow. I checked that each pair differs with `math/big.Rat`.

The response says a yes covers the arguments as they stood when asked. Its added numeric fix should equate different spellings of one value, not different values. This is separate from the recorded limit on sets after a question.

Test: `TestReviewCodexGapConfirmationRoundedNumbers`. Ran with the gap flag and `-race`. Distinct numbers and the changed outgoing JSON were observed. Their meaning to an exact-number upstream is inferred.

## C5. Nested skills can promise different bytes for one file

Severity: medium. Engine only.

Two static skills share `skill://demo/sub/x.md`, whose text is `hunter2`. The outer skill is `skill://demo/SKILL.md`; the inner is `skill://demo/sub/SKILL.md`. Both manifests list the shared file and hold its text. Their own markdown files have valid frontmatter named `demo` and `sub` respectively.

Rule:

```text
resources/read.post.result
when: true
then: response.result.contents:
  response.result.contents.map(c, {'uri': c.uri,
    'text': c.text.replace('name: demo', 'name: renamed')
                  .replace('hunter2', 'REDACTED')})
```

Get the inner skill, then read the shared file. The inner manifest publishes the SHA-256 digest of `REDACTED`. The read returns `hunter2`. The outer plan rejects its name change and keeps all original bytes. `vouchedBy` chooses that outer skill, while the inner listing independently publishes its successful plan.

A related case has a dynamic outer skill and a static inner manifest listing that same file. With `when: true` and `response.result.contents: response.result.contents.map(c, {'uri': c.uri, 'text': 'REDACTED'})`, the read returns `REDACTED`. `vouchedBy` prefers a static manifest over its remembered dynamic ancestor.

The result-hook docs promise that a manifest always matches reads. They also say every file beneath a dynamic skill is served unchanged. Choosing one skill in `resources.go:487` cannot enforce both promises across overlapping skills.

Tests: `TestReviewCodexGapNestedSkillPlans` and `TestReviewCodexGapDynamicOuterSkill`. Ran with the gap flag and `-race`. The manifest digest and returned bytes were checked.

## C6. Resource result hooks lose fields the server supplied

Severity: medium. Valid reads can be refused by rules that read present fields.

Rule:

```text
resources/read.post.result
when: response.result.resultType == 'block'
then: decision: 'deny'
```

Request: read `docs://a`. Response:

```json
{"resultType":"complete","_meta":{"label":"safe"},
 "contents":[{"uri":"docs://a","text":"OK"}]}
```

The rule compiled, then failed because `resultType` was absent. The read returned `-32602`. Replacing the condition's left side with any of these gave the same failure:

```text
response.upstream.result.resultType
response.result._meta.label
response.upstream.result._meta.label
```

`readRules` in `resources.go:515` rebuilds both result maps with `contents` only. This discards fields that the tree allows and that the actual response holds. The response to finding 7 says `resultType` was added to the tree. The overview describes both result branches as their corresponding messages. For `_meta` in the gateway, the key must also be on its output allow-list.

Test: `TestReviewCodexGapReadResponseFields`. Ran all four variants with the gap flag and `-race`. This is an engine result, not an HTTP metadata-allowlist test.

## C7. A prompt forwards after session rules spend its budget

Severity: medium. A final enforce-budget check is missing from `GetPrompt`.

Catalog: prompt `p`. Engine setting: `Limits{Budget: time.Nanosecond}`. Rule:

```text
server.session
when: true
then: session._meta: true
```

Request: `prompts/get`, `{"name":"p"}`. It forwarded with `ForwardMeta: true` after the only rule spent the allowance. The small allowance makes the missing check reproducible without a slow rule.

`GetPrompt` checks `expired(ctx)` before `Connect`, then forwards without checking again (`resources.go:166`). Tool calls and resource reads check after `Connect`. The overview promises `-32603` when enforce rule time exceeds the request budget. An overrun during session rules should have that outcome too.

Test: `TestReviewCodexGapPromptSessionBudget`. Ran with the gap flag and `-race`. The missing check is also directly visible in the code. A default two-second overrun was not induced.

## C8. Another server's outage blocks an explicit mux read

Severity: medium.

Rules: none. Entitlement `a` is available but does not list `docs://h/unlisted`. Entitlement `b` has `Unavailable: true`. Both are in the caller's mux request.

Request: `resources/read`, `{"uri":"a+docs://h/unlisted"}`.

The result was `-32602`: `cannot resolve a+docs://h/unlisted: b unavailable; retry with a prefixed URI`. The request already has the required prefix.

`route` in `resources.go:760` tries fallback when `a` does not claim the unlisted URI. That speculative lookup makes `b`'s outage block the explicit route. The overview permits unlisted reads with a slug prefix and says unavailable fallback candidates can be avoided by using one.

Test: `TestReviewCodexGapExplicitMuxReadBlockedByOtherServer`. Ran with the gap flag and `-race`. Unavailability was supplied as engine state; no network failure was induced.

## C9. Confirm interpolation rejects valid CEL strings

Severity: low. The text form refuses valid rules.

Exact rules, each independently on `tools/call.pre`:

```text
when true confirm "Value {'''don't'''}?"
when true confirm "Value {r'\\'}?"
```

Both fail compilation with `the question has a { with no closing }`. There is no request-time result because saving fails. The intended request is a call to any tool matching `when true`.

I compiled the equivalent engine expressions successfully:

```text
elicit('sure', 'Value {v}?', {'v': '''don't'''})
elicit('sure', 'Value {v}?', {'v': r'\'})
```

The outer text string uses two backslashes to produce the one backslash in the raw CEL literal. `confirmCall` in `ruletext.go:260` handles neither raw nor triple-quoted strings when locating the closing brace. The response to finding 20 says strings inside question expressions are handled. This is a further case beyond the ordinary quoted strings already fixed.

Test: `TestReviewCodexGapQuestionCELStrings`, below. Ran through a Go overlay outside the repository, with the gap flag. Both engine controls passed and both text assertions failed. Additional action-combining probes were rejected as expected.

## Verification and limits of the runs

The source checkout is at `ba03834`. It had a pre-existing untracked file outside scope; it was not opened or modified. Broad tests and scenario execution used an allowlisted copy at `/tmp/bifrost-review-codex-l4dp274l`. The copy omitted the prohibited paths before opening source files. Existing tests and scenarios would otherwise read the prohibited real-fixture directory. No such fixture was read.

The requested broad command was run with socket creation blocked by seccomp and with `GOPROXY=off GOSUMDB=off`:

```sh
go test ./pkg/... ./internal/... ./cmd/... -race -count=1
```

It did not pass. Local HTTP harness, gateway, skill-server and OAuth tests could not open sockets. `TestRegexReplaceOnLargeNotebook` also failed with `telemetry pin missing`: its fallback schema lacks the fixture's `telemetry` parameter, but the test still expects the pin. This is a test-environment limitation, not evidence of a new engine regression.

These requested commands succeeded in that source copy:

```sh
go build -o bin/hookctl ./cmd/hookctl
./bin/hookctl run -summary pkg/hooks/testdata/scenarios/*.json
./bin/hookctl hooks tools/call.pre
./bin/hookctl schema tools/call.pre
```

Scenarios: 514 passed, zero failed, four skipped because excluded real fixtures were absent. Both printed documents parsed as JSON. I inspected their request tree and schema; I did not validate every runtime document against every schema.

The new normal-mode tests pass with `-race`. Every new engine gap test calls `knownGap(t)` first. With `HOOKS_KNOWN_GAPS=1`, all ten engine gap tests reproduce their reported failure, including the two tests grouped under C2 and C5. The text gap test also reproduced both cases. No race report occurred.

The unfiltered `go test ./pkg/hooks/ -count=1` was also run in the safe copy. It fails only at the existing notebook test described above. The core suite passed with that one exclusion, including under `-race`:

```sh
go test ./pkg/hooks/ -race -count=1 -skip '^TestRegexReplaceOnLargeNotebook$'
go test ./pkg/hooks/ -count=1 -skip '^TestRegexReplaceOnLargeNotebook$'
```

No existing test or fixture was edited to make it pass. This restriction prevents claiming the user's unfiltered-green requirement was met.

## Checks that held

- Ran: 16 concurrent answers to one confirmation key produced exactly one forward, under `-race`.
- Ran existing review tests: caller, tool, entitlement, generation, method and expiry binding remained enforced outside C4's numeric collisions.
- Ran existing review tests: changed questions were asked again; a new gateway question during an upstream answer round was refused.
- Ran existing review tests: prompt and resource confirmations, including directory reads, remained take-once.
- Ran: unknown whole-map annotation keys failed at runtime on both resource and template listings.
- Ran: malformed escaped setters were refused; `arguments.a\.b` set the literal `a.b` key while preserving nested `a.b`.
- Ran: ordinary guards and shadowed macro names compiled; guards did not hide invalid plain paths.
- Ran: malformed percent paths, encoded unreserved path characters, dot segments, duplicate slashes and exact default ports were refused on both routes.
- Ran: canonical opaque `urn:` and `data:` forms, custom authority case, `git+ssh:`, reserved escapes and URL-valued queries passed unchanged.
- Ran: exact mux fallback resolved one claimant and refused two claimants as ambiguous.
- Ran: a failed tool list rule excluded the tool from search, definitions and execution.
- Ran: task handles were refused for tools, prompts and resource reads; a rule could not enable tasks.
- Ran: a low CEL cost allowance refused a call; cancellation and an exhausted enforce allowance refused listings.
- Ran: absent progress fields and explicit progress denies dropped events on all three progress hooks; the final response still completed.
- Ran existing engine tests: panic recovery, audit isolation and ordinary evaluation failures remained closed on their tested surfaces. Server session and discovery failures retain their documented fallback behavior.
- Ran text probes: combined actions were rejected, normal comments ended on their line, and ordinary quoted braces compiled.
- Read: the gateway's state adapter uses `CacheTake` and refuses missing or unreadable state; no live database concurrency test was run.

## Not covered

No deployed gateway, external server, DNS, HTTP transport or database was contacted. The HTTP suites failed at socket creation; their assertions are not claimed as covered. Python and TypeScript SDK environments were not installed. Skill findings exercise the engine, not deployed skill integration. There was no exhaustive URI-parser survey, scheme-specific equivalence analysis for opaque URIs, or decision on relative references without a scheme. The resource alias matrix did not cover every directory-result shape. I did not exhaustively fuzz guards, nested native values, cost estimators or every panic site. The web editor, sustained load, and every hook document/schema pair were not reviewed.

## Text-layer reproduction

Save this outside the repo. Map a synthetic test filename under `internal/ruletext` to it with `go test -overlay`. It adds no repository file. Run with `HOOKS_KNOWN_GAPS=1`:

```go
package ruletext

import (
    "fmt"
    "os"
    "testing"

    "github.com/andyjmorgan/Bifrost/pkg/hooks"
)

func knownGap(t *testing.T) {
    t.Helper()
    if os.Getenv("HOOKS_KNOWN_GAPS") != "1" {
        t.Skip("set HOOKS_KNOWN_GAPS=1")
    }
}

func TestReviewCodexGapQuestionCELStrings(t *testing.T) {
    knownGap(t)
    for _, tc := range []struct{ text, expression string }{
        {`when true confirm "Value {'''don't'''}?"`, `'''don't'''`},
        {`when true confirm "Value {r'\\'}?"`, `r'\'`},
    } {
        _, p := hooks.Compile([]hooks.Rule{{
            Hook: "tools/call.pre", When: "true",
            Then: map[string]string{"confirm": fmt.Sprintf(
                "elicit('sure', 'Value {v}?', {'v': %s})", tc.expression)},
        }})
        if p != nil {
            t.Fatalf("engine control: %v", p)
        }
        if _, p := Compile(map[string]string{"tools/call.pre": tc.text}); len(p) != 0 {
            t.Errorf("valid CEL rejected in %s: %v", tc.text, p)
        }
    }
}
```
