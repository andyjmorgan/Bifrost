# Verification of the fixes to the review findings

Written November 17, 2026. Branch `rules-hooks`, commit b00ef36. It checks the fixes described in `review-response.md` against the findings in `review-findings.md`.

## Verdict

The confirmation fixes hold, with one limit. Every state key works once, an answer counts only for its own question, and what a question shows cannot be swapped. I could not replay or move a confirmation on any method. The limit is that an argument set after the question is not covered. Seven other fixes are partial: findings 3, 4, 5, 9, 10, 11 and 16. The URI fix is the serious one: a scheme in capitals is now refused, but the same deny is still passed by writing `file:/secrets/key`, `file://localhost/secrets/key`, a leading space, a fragment, the host in capitals and other spellings, 19 in all. The `set` key fix stops an escape in the branch and not an escape after it. The type check still saves `null`, the path check still saves wrong names through several forms, and the note on unguarded reads still misses and misfires. The fix for skill files opened one new way to change a file a manifest vouches for. I found 15 new problems: 1 critical under stated conditions, 2 medium and 12 low.

## How to run the tests

- Tests that pass: `go test ./pkg/hooks/ -run 'TestReviewVerify' -count=1`
- Tests that fail today: `HOOKS_KNOWN_GAPS=1 go test ./pkg/hooks/ -run 'TestReviewVerifyGap' -count=1`
- Text-layer tests: the bodies are at the end of this report. Save them as a file outside the repo and run them with `go test -overlay`, as shown there.

All engine tests are in `pkg/hooks/review_verify_test.go`. There are 6 that pass and 13 that fail today. Each failing one calls `knownGap(t)` first. `go test ./pkg/hooks/ -count=1` is ok with the new file.

## The original findings

| No. | Verdict | Evidence |
| --- | --- | --- |
| 1 | Holds | Two gateway questions and the server's question: each of the three keys forwarded once, then `invalid request state`. Same on `prompts/get`, `resources/read` and a directory read, and through a store that writes state as JSON. `TestReviewVerifyEveryStateKeyWorksOnce` |
| 2 | Partly | What the question shows and the arguments at the question are covered. `TestReviewVerifyAQuestionCoversWhatItShows`. An argument a rule sets after the question is not: see V5 |
| 3 | Does not hold beyond the scheme | A scheme in capitals is refused on both routes and with no `//`. `TestReviewVerifyASchemeInCapitalsIsRefusedEverywhere`. 19 other spellings pass the same denies: see V1 |
| 4 | Partly | An escape in the branch is refused. An escape after the branch is saved and does nothing on five hooks: see V2 |
| 5 | Partly | `tool.annotations.risk` is refused. `resource.annotations` set as a whole map still takes a key MCP does not name and loses it: see V8 |
| 6 | Recorded, not fixed | `TestReviewGapAZeroTheServerSentIsAbsent` still fails with `HOOKS_KNOWN_GAPS=1`. Not attacked again |
| 7 | Holds for the parts fixed | Scratch run: `response.result.resultType` is read, `elicitation.upstream.method` is refused when saved, a directory read shows `resources/directory/read`. Related: V9 |
| 8 | Holds | 23 valid rules with nested macros, two variables, map macros, `cel.bind` chains and shadowing all save. `TestReviewVerifyTheSaveCheckTakesValidRules`. Cost of the fix: V9 |
| 9 | Partly | The first reviewer's cases hold. Four other forms get no note and four guarded forms get one: see V10 |
| 10 | Partly | Scalars of the wrong type are refused and 23 valid values are not. `TestReviewVerifyTheTypeCheckTakesValidValues`. `null`, a zero duration and a list of the wrong things are still saved: see V7 |
| 11 | Partly | The stated case holds. The fix opened V6. A related loss is V14 |
| 12 | Holds | Existing test passes. I read `sent` in `UpstreamReadReply`. Not attacked further |
| 13 | Holds | Scratch run: `@mux`, `` `@mux` `` and `` `@slugUris` `` are syntax errors |
| 14 | Recorded | Existing test passes. Not attacked again |
| 15 | Holds | Existing test passes. Not attacked further |
| 16 | Partly | A value a rule sets is checked. An escaped key skips the check (V2). An entry a server sends that is not an object is made into one (V12) |
| 17 | Holds | No signer is left in the code (searched). State a client wrote is refused |
| 18 | Holds | Searched non-test Go for `has(input`, `args.` in messages and `request.confirmations`: none |
| 19 | Holds on one line | 12 one-line forms with `//` in single, double, triple-quoted and raw strings. A string that runs over lines is cut: see V13 |
| 20 | Holds | Deny with confirm is refused. Braces and quotes inside strings in a question are read correctly. The shared answer breaks with a set between: see V4. An unknown escape loses its backslash: see V15 |
| 21 | Holds, as read | I read `callerMap` and `credentialHeader` and ran the gateway tests. Names such as `x-api_key`, `x-jwt` and `x-pat` do not match the list. That is read, not run |

## New problems

### V1. Other spellings of a URI pass a deny

Severity: critical, under the conditions stated. Same class as finding 3.

Rules:

```
resources/list.post            when: resource.uri.startsWith('file:///secrets/')        then: decision: 'deny'
resources/list.post            when: resource.uri == 'file:///listed/secret.txt' || resource.uri == 'https://wiki.example/private' || resource.uri == 'file:///listed/a%2Fb'   then: decision: 'deny'
resources/templates/list.post  when: template.uriTemplate.startsWith('file:///vault/')  then: decision: 'deny'
```

Catalog: resources `file:///listed/secret.txt`, `https://wiki.example/private` and `file:///listed/a%2Fb`, and a template `file:///vault/{name}`.

Request: `resources/read` of each URI below, on the direct route and on the mux with `a+` in front.

What happened: every one was forwarded as typed. The plain forms were refused.

| Spelling | Prefix deny | Exact deny of a listed resource | Denied template |
| --- | --- | --- | --- |
| Scheme in capitals | refused | refused | refused |
| No `//`: `file:/secrets/key` | passed | passed | passed |
| `file://localhost/…` | passed | passed | passed |
| A leading space, or a tab inside the scheme | passed | not run | not run |
| Backslashes: `file:\\\secrets\key` | passed | not run | not run |
| No `//` with a dot segment or `%73`: `file:/secrets/../secrets/key`, `file:/%73ecrets/key` | passed | not run | not run |
| Fragment `#x` or query `?x=1` | `#x` refused, the prefix still matches | passed | passed |
| Trailing slash | not run | passed (scratch run) | passed (scratch run) |
| Host in capitals, `:443`, `user@`, trailing dot | not run | passed (the last two in a scratch run) | not run |
| Percent hex in the other case: `%2f` for `%2F` | not run | passed | not run |
| `%2F` for a slash | passed (scratch run), documented as opaque | not run | refused |
| `%2E%2E` | refused, before any rule runs | not run | not run |
| A scheme with `+` on the mux: `a+git+SSH://`, `a+GIT+ssh://` | refused | not run | not run |

A read result has the same gap. A server's entry for `file:/secrets/key` or `file://localhost/secrets/key` was passed to the client while its entry for `file:///secrets/key` was dropped.

Why: `nonCanonical` checks the scheme's case and then returns at once for a URI with no `://`. It never looks at the host, the query or the fragment. `normalizeURI` and the denied-template match compare text.

Conditions: the server resolves the URI with a URL parser. Go's `net/url` gives the same path for `file:/x` and `file:///x` and drops a fragment, and a path with a dot segment is the same path once cleaned; the test checks that. Node 22's `URL` folds `file:/x`, `file://localhost/x`, a leading space, a tab, backslashes, an `https` host's case and `:443` to the plain form; I ran that. The TypeScript SDK's `McpServer` (1.29.0) looks a resource up by `new URL(request.params.uri).toString()`; I read that in a local copy and did not run a server.

What should happen: the docs say a prefix deny covers unlisted URIs alike, and a URI not in canonical form is refused. The gateway should refuse these spellings, or compare denies against one normal form.

Test: `TestReviewVerifyGapOtherSpellingsOfAURIPassADeny`.

### V2. An escape after the branch in a `set` key is accepted and does nothing

Severity: medium. Finding 4 again, one segment later. It needs a rule in the engine's JSON form.

Rule, on `tools/call.post.result`: `when: true`, `then: "response.result.conten\t": [{'type': 'text', 'text': 'REDACTED'}]`.

Request: call `t`. The server returns `{"content":[{"type":"text","text":"SECRET"}]}`.

What happened: the rule saved. The client got `SECRET`, and a second key named `conten\t`. The hit says `set`.

The same on other hooks:

- `"response.result.structuredConten\t": 'not an object'`: sent to the client. The check of the value's form is keyed on the plain name and was skipped.
- `server.settings`, `"settings.tool\s": false`: tools stayed listed.
- `tools/call.post.elicit`, `"elicitation.params.messag\e": 'SAFE'`: the server's message went out unchanged.
- `server/discover.post`, `"server.titl\e": 'SAFE'`: no title.
- `server.session`, `"session._met\a": true`: `_meta` was not forwarded.

On `tools/list.post` and `tools/call.pre` an escaped key works as the plain one, because those setters split the key the same way.

What should happen: a key that is not the path as written is refused when saved.

Test: `TestReviewVerifyGapEscapeAfterTheBranchInASetKey`.

### V3. A gateway question asked on the round that answers a server's question drops the server's round

Severity: low.

Rules, on `tools/call.pre`:

```
when: true                                          then: confirm: elicit('a', 'A?')
when: request.params.?_meta.?late.orValue(false)    then: confirm: elicit('b', 'B?')
```

Request: call `t`, answer `a`, the call is forwarded and the server asks its own question. Send the answer to the server's question with `_meta` `{"late": true}`. The gateway asks `B?`. Answer it.

What happened: the call was forwarded with no `requestState` and no `inputResponses`. The state of the new question does not carry the server's state or the user's answer to the server. The server is called as if for the first time.

What should happen: the server's state and the answer are kept, or the round is refused. The docs say the gateway and the server never ask in the same round.

Test: `TestReviewVerifyGapAGatewayQuestionDropsTheServersRound`.

### V4. The same question twice with a set between can never be answered

Severity: low. It fails closed.

Text, on `tools/call.pre`:

```
when true
  confirm "Sure?"
when true
  set request.params.arguments.q = 'changed'
when true
  confirm "Sure?"
```

Request: call `t`, then answer the question.

What happened: `The request changed since the confirmation was asked. Send it again.` Sending it again gives the same. The two lines share a key, so the first answer is taken as an answer to the second for other arguments.

What should happen: the docs say two rules that ask the same question under the same key share one answer. Either that, or the second is asked.

Tests: `TestReviewVerifyGapTheSameQuestionTwiceWithASetBetween`, and the text-layer test of the same name below.

### V5. What a rule sets after a question is not covered

Severity: low. No value the user was shown can be swapped.

Rules, on `tools/call.pre`:

```
when: true   then: confirm: elicit('sure', 'Deploy?')
when: true   then: request.params.arguments.env: request.params.?_meta.?env.orValue('staging')
```

Request: call `deploy` with `{}` and `_meta` `{"env":"staging"}`. Answer with `_meta` `{"env":"production"}`.

What happened: forwarded with `{"env":"production"}`. The second rule does not run on the question round, so its value is in no binding. The same with a header. Client `_meta` that `session._meta: true` forwards can also differ between the two rounds.

What should happen: the docs tell authors to ask first and then set for `request.time`. They should say that what is set after a question is not confirmed, or the engine should bind what it forwards.

Test: `TestReviewVerifyGapWhatIsSetAfterAQuestionIsNotCovered`.

### V6. A file the outer skill's manifest lists is changed when it sits under a nested skill

Severity: medium. Opened by the fix for finding 11. The gateway in this repository does not load skills yet, so this is the engine only.

Catalog: skill `skill://demo/SKILL.md` whose manifest lists `skill://demo/sub/x.md`. A nested skill `skill://demo/sub/SKILL.md` whose manifest lists only its own `SKILL.md`.

Rule, on `resources/read.post.result`: `when: request.caller.role == 'member'`, `then: response.result.contents: response.result.contents.map(c, {'uri': c.uri, 'text': 'EVIL'})`.

Request: read `skill://demo/sub/x.md`.

What happened: the client got `EVIL`. `skills/list` still shows the server's digest for that file. The file is looked up in the deepest skill's manifest only.

What should happen: a file any visible skill's manifest lists keeps its bytes.

Test: `TestReviewVerifyGapAFileTheOuterManifestListsIsChanged`.

### V7. A constant of the wrong type is still saved in several forms

Severity: low. Each fails closed and takes the whole hook with it.

Saved: `tool.title: null`, `tool.outputSchema: null`, `tool.annotations.readOnlyHint: null`, `settings.tools: null`, `session.clientInfo: null`, `timeout: null`, `timeout: duration('0s')`, `timeout: duration('-5s')`, `tool.title: optional.of(5)`, `response.result.content: [1]`, `response.result.structuredContent: 'a string'`, `session.clientInfo: {1: 2}`.

What happened at request time: with `tool.title: null` no tool is listed (`expected string, got <nil>`). With `timeout: null` every call is refused. With `settings.prompts: null` every switch goes off. The comment on `fits` says null fits everything because it removes a key. `checkType` refuses null on every path with a type.

What should happen: refused when saved, or null removes the key as the comment says.

Test: `TestReviewVerifyGapWrongConstantsStillSaved`.

### V8. A resource annotation MCP does not name is set through the whole map and lost

Severity: low. Finding 5 on resources and templates.

Rules, on `resources/list.post`: `resource.annotations: {'risk': 'high', 'priority': 0.5}`, then `resource.description: 'risk=' + dyn(resource.annotations).?risk.orValue('lost')`.

What happened: the listing has `description: risk=high` and `annotations: {"priority":0.5}`.

What should happen: the map's keys are checked, as the single keys are.

Test: `TestReviewVerifyGapUnknownAnnotationThroughTheWholeMap`.

### V9. A key a server sent on a read entry can no longer be read by name

Severity: low. A valid rule is refused. It follows from the fix for findings 8 and 9.

Rule, on `resources/read.post.result`: `when: response.result.contents.exists(c, has(c.annotations))`.

What happened: refused when saved, `response.result.contents[] has no "annotations"`. A server entry that carries `annotations` is passed to the client and is in the document the rule reads. Before the fix a macro variable was not walked and this rule saved.

What should happen: the entry is an open map in the tree, like a tool's content block, or the gateway drops the key.

Test: `TestReviewVerifyGapAKeyTheServerSentCannotBeRead`.

### V10. The note on an unguarded read still misses and misfires

Severity: low.

No note, and each fails on a tool with no title:

- `has(tool.title) || tool.title == 'x'`
- `has(tool.title) ? false : tool.title == 'x'`
- `when: tool.name == 'a' || has(tool.title)` with `then: tool.description: tool.title`
- `response.result.content.exists(c, has(c.text)) && response.result.content.exists(c, c.text.contains('x'))`

A note on a guarded read:

- `!has(tool.title) || tool.title == 'x'`
- `!has(tool.title) ? false : tool.title == 'x'`
- `response.result.content.all(c, !has(c.text) || !c.text.contains('x'))`
- `tool.?title.hasValue() && tool.title == 'x'`

Why: a `has()` anywhere in the expression clears the note for that path, and a `has()` under `!` never does.

Test: `TestReviewVerifyGapNotesStillMissAndMisfire`.

### V11. Wrong names are still saved through forms the path check does not follow

Severity: low. Each fails when a request arrives, which denies.

Saved on `tools/call.post.result`: `response.result.content.exists(i, c, i.type == 'text')` where `i` is the index, `response.result.content.filter(c, c.type == 'text')[0].nosuch`, `cel.bind(r, true ? response : response, r.nosuch == 1)`, `(true ? response : response).nosuch`, `[response][0].nosuch`, `{'a': response}.a.nosuch`, `optional.of(response).value().nosuch`, `response['res' + 'ult'].nosuch`. On `tools/list.post`: `tool.params.exists(n, p, n.type == 'string')` where `n` is the name.

What should happen: the docs say every path is checked when the rule is saved. Either that, or the docs name the forms that are not.

Test: `TestReviewVerifyGapWrongPathsStillSaved`.

### V12. A read entry that is not an object is made into one

Severity: low.

Request: read `docs://a`. The server returns `{"contents":[{"uri":"docs://a","text":"A"},"junk",5,null,{"uri":5,"text":"other"}]}`. No rules.

What happened: the client got five entries. The string, the number and the null each became `{"uri":"docs://a"}`. The entry with a number for its URI became `{"uri":"docs://a","text":"other"}`.

What should happen: dropped with an issue, or the read refused.

Test: `TestReviewVerifyGapAnEntryThatIsNotAnObjectIsMadeUp`.

### V13. A `//` inside a string that runs over lines is cut as a comment

Severity: low. That it compiled to the full text before the fix for finding 19 is inferred from reading the fix.

Text, on `tools/list.post`:

```
when tool.description.contains('''see
  http://x.example
  ''')
  deny
```

What happened: no problem, and `when: tool.description.contains('''see http: ''')`. With single quotes the same text is a syntax error, which is loud.

Test: `TestVerifyGapACommentCutInsideAStringThatSpansLines`, below.

### V14. A manifest file the catalog holds no text for is not rewritten

Severity: low. Engine only, as for V6.

Catalog: a skill whose manifest lists `skill://demo/extra.md`, with no text held for it.

Rule, on `resources/read.post.result`: `when: true`, `then: response.result.contents: response.result.contents.map(c, has(c.text) ? {'uri': c.uri, 'text': c.text.replace('hunter2', '[redacted]')} : c)`.

Request: read `skill://demo/extra.md`. The server returns `extra hunter2`.

What happened: the client got `extra hunter2`. The hit says `set` and no issue is recorded. The plan skips a file with no held text, and the read puts the server's bytes back.

What should happen: the rewrite is applied, or the listing and the read say it was not.

Test: `TestReviewVerifyGapAManifestFileWithNoHeldTextIsNotRewritten`.

### V15. An unknown escape in a question loses its backslash

Severity: low.

Text, on `tools/call.pre`: `confirm "Caf\u00e9?"`.

What happened: no problem, and the question is `Cafu00e9?`. CEL reads `\u00e9` as a letter; the text layer's own reader drops the backslash.

What should happen: the escape is read as CEL reads it, or refused.

Test: `TestVerifyGapAnEscapeInAQuestionLosesItsBackslash`, below.

## Statements in the docs that the code contradicts

- `overview.md`, "To close a folder, deny its prefix … That covers listed resources, templates, deeper paths and unlisted URIs alike." See V1.
- `overview.md`, "The gateway checks every path in a rule against the hook's tree when the rule is saved. A wrong name never waits for a request to be found." See V11.
- `overview.md`, "A value whose type is known when the rule is saved must fit its path." See V7.
- `overview.md` and `tools-call-post-elicit.md`, "Gateway confirmations … always resolve before step 6, so the gateway and the server never ask for input in the same round." See V3.
- `tools-call-pre.md`, "Two rules that ask the same question under the same key share one answer." See V4.
- `resources-read-post-result.md`, "On a read, `SKILL.md` and each file that the skill's manifest lists keep the bytes the manifest describes, whatever a rule sets at read time." See V6.
- `tools-call-pre.md`, "cannot be answered for one value and forwarded with another" is true for set-then-ask. The page does not say that a set after the question is not covered. See V5.
- Code comments, not docs: `argsHash` says two encodings of the same number hash alike. `1` and `1.0` do not, and the answer is refused. `fits` says null removes a key. See V7.

## What I ran and what I only read

Ran:

- Every test named above, with and without `HOOKS_KNOWN_GAPS=1`.
- `go test ./pkg/hooks/ ./internal/ruletext/ ./internal/gateway/ -count=1`: ok.
- `hookctl run -summary` over the scenarios, doc checks and e2e cases: 685 pass, 0 fail, 103 skip.
- `hookctl hooks tools/call.pre`: `request` has no `confirmations`.
- Scratch probes of about 170 rules through the save check, 38 URI spellings on both routes, and 50 texts through the text layer.
- Node 22's `URL` and Go's `net/url` on the spellings in V1.

Read or inferred:

- The TypeScript SDK's lookup by `new URL(uri).toString()`. No MCP server was run.
- `cacheStates.Take` and the Postgres `delete … returning` behind it. It looks atomic. Not run.
- Finding 21's header list and `callerMap`.
- That V13 is a regression.

## Not covered

- Real HTTP, real MCP servers, and the Go, Python and TypeScript client SDKs.
- The suite under `-race`, and two answers racing for one state key.
- Findings 6, 12, 14 and 15 beyond their existing tests.
- Progress hooks, search mode, tasks, budgets and cost limits.
- `hookctl schema` against documents, beyond the probes for finding 7.
- `ruletext.Lint`, the saved-text corpus and the web editor.
- Doc pages other than `overview.md`, `tools-call-pre.md` and `resources-read-post-result.md`.
- `skills/get`, and directory reads beyond one spelling and the confirmation checks.

## Text-layer tests

Save as `zz_verify_test.go` outside the repo and run:

```
printf '{"Replace":{"%s/internal/ruletext/zz_verify_test.go":"%s"}}' "$PWD" /path/to/zz_verify_test.go > /tmp/overlay.json
go test -overlay /tmp/overlay.json ./internal/ruletext/ -run TestVerify -count=1 -v
```

The first test passes. The other three fail today.

```go
package ruletext

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/andyjmorgan/Bifrost/pkg/hooks"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Passes. Findings 19 and 20 hold on every one-line form tried.
func TestVerifyCommentsAndQuestionsOnOneLine(t *testing.T) {
	for text, want := range map[string]string{
		"when tool.name == 'a' // or the other one\n  || tool.name == 'b'\n  deny\n":             "tool.name == 'a' || tool.name == 'b'",
		"when tool.name == 'a' // don't hide b\n  deny\n":                                        "tool.name == 'a'",
		"when tool.name == \"a//b\" || tool.name == 'c//d' // x 'y\n  deny\n":                    "tool.name == \"a//b\" || tool.name == 'c//d'",
		"when tool.name == '''a//b''' || tool.name == \"\"\"c // d\"\"\" // x\n  deny\n":         "tool.name == '''a//b''' || tool.name == \"\"\"c // d\"\"\"",
		"when tool.name.matches(r'^a\\\\') // raw, ends in a backslash\n  deny\n":                "tool.name.matches(r'^a\\\\')",
		"when tool.name == 'it\\'s // fine' // c\n  deny\n":                                      "tool.name == 'it\\'s // fine'",
		"when tool.name in ['r','\\'//x'] // c\n  deny\n":                                        "tool.name in ['r','\\'//x']",
		"when tool.description.contains('a') ||\n  // a comment line\n  tool.name == 'b'\n  deny\n": "tool.description.contains('a') || tool.name == 'b'",
		"// first\nwhen size(tool.name) / 2 > 1// tight\n  deny//tight\n":                         "size(tool.name) / 2 > 1",
		"when true deny 'http://policy.example/why' // inline\n":                                 "true",
		"audit when tool.name == 'a' // c\n  deny // c\n":                                         "tool.name == 'a'",
	} {
		rules, probs := Compile(map[string]string{"tools/list.post": text})
		if len(probs) > 0 || len(rules) != 1 || rules[0].When != want {
			t.Errorf("%q: problems %v, rules %+v", text, probs, rules)
		}
	}
	rules, probs := Compile(map[string]string{"tools/list.post": "when true // c\n  set tool.description = 'http://x' // c\n    + '//y' // d\n"})
	if len(probs) > 0 || rules[0].Then["tool.description"] != "'http://x' + '//y'" {
		t.Errorf("a comment after a set value: %v %+v", probs, rules)
	}
	for text, want := range map[string]string{
		`confirm "Run {request.params.arguments['}']}?" // c`:  `elicit("confirm_32a5b98a", "Run {v1}?", {"v1": request.params.arguments['}']})`,
		`confirm "Use {tool.name + '}' + \"{\"}?"`:             `"v1": tool.name + '}' + "{"})`,
		`confirm 'With {{"a": 1}.a} and {tool.name}'`:          `{"v1": {"a": 1}.a, "v2": tool.name})`,
		`confirm "Tern {true ? '{' : '}'}"`:                    `{"v1": true ? '{' : '}'})`,
		`confirm "Go to http://x.example/{tool.name}?"`:        `"Go to http://x.example/{v1}?", {"v1": tool.name})`,
		`confirm "100% sure? It's \"fine\" \\ ok {tool.name}"`: `"100% sure? It's \"fine\" \\ ok {v1}", {"v1": tool.name})`,
	} {
		rules, probs := Compile(map[string]string{"tools/call.pre": "when true\n  " + text + "\n"})
		if len(probs) > 0 || !strings.HasSuffix(rules[0].Then[hooks.KeyConfirm], want) {
			t.Errorf("%s: problems %v, rules %+v", text, probs, rules)
		}
	}
	for _, text := range []string{"confirm \"A?\"\n  deny \"no\"", "confirm \"A?\"\n  confirm \"B?\"", `confirm "Unbalanced {tool.name?"`, `confirm "Empty {}"`, `confirm "A?" + "B"`, `confirm "{tool.name} then {v1}"`} {
		if _, probs := Compile(map[string]string{"tools/call.pre": "when true\n  " + text + "\n"}); len(probs) == 0 {
			t.Errorf("%s: compiled", text)
		}
	}
}

// Fails today. A // inside a string that runs over more than one line is
// cut as a comment, and the rule compiles with other text in the string.
func TestVerifyGapACommentCutInsideAStringThatSpansLines(t *testing.T) {
	rules, probs := Compile(map[string]string{"tools/list.post": "when tool.description.contains('''see\n  http://x.example\n  ''')\n  deny\n"})
	if len(probs) == 0 && !strings.Contains(rules[0].When, "http://x.example") {
		t.Errorf("compiled with no problem to %q", rules[0].When)
	}
}

// Fails today. An escape the text layer does not know loses its backslash,
// so the user is asked "Cafu00e9?".
func TestVerifyGapAnEscapeInAQuestionLosesItsBackslash(t *testing.T) {
	rules, probs := Compile(map[string]string{"tools/call.pre": "when true\n  confirm \"Caf\\u00e9?\"\n"})
	if len(probs) == 0 && strings.Contains(rules[0].Then[hooks.KeyConfirm], "Cafu00e9") {
		t.Errorf("compiled with no problem to %s", rules[0].Then[hooks.KeyConfirm])
	}
}

// Fails today. Two confirm lines with the same words share a key. With a
// set between them the second can never be answered.
func TestVerifyGapTheSameQuestionTwiceWithASetBetween(t *testing.T) {
	rules, probs := Compile(map[string]string{"tools/call.pre": "when true\n  confirm \"Sure?\"\nwhen true\n  set request.params.arguments.q = 'changed'\nwhen true\n  confirm \"Sure?\"\n"})
	if len(probs) > 0 {
		t.Fatalf("problems: %v", probs)
	}
	set, eps := hooks.Compile(rules)
	if eps != nil {
		t.Fatalf("problems: %v", eps)
	}
	s := &hooks.Server{Slug: "a", Generation: "1", Info: hooks.ServerInfo{ProtocolVersion: hooks.ProtocolCurrent}, Rules: set,
		Catalog: hooks.Catalog{Tools: []*mcp.Tool{{Name: "t", InputSchema: map[string]any{"type": "object"}}}}}
	r := hooks.Request{Route: "direct", Servers: []*hooks.Server{s}, Principal: "p", Client: map[string]any{"capabilities": map[string]any{"elicitation": map[string]any{}}}}
	e, ctx := &hooks.Engine{}, context.Background()
	p := &mcp.CallToolParamsRaw{Name: "t", Arguments: json.RawMessage(`{}`)}
	for round := 1; round <= 4; round++ {
		o := e.CallTool(ctx, r, p)
		if o.Forward != nil {
			return
		}
		ir, asked := o.Result.(hooks.InputRequired)
		if !asked {
			b, _ := json.Marshal(o.Result)
			t.Fatalf("round %d: refused, and no question to answer: %s", round, b)
		}
		in := mcp.InputResponseMap{}
		for k := range ir.InputRequests {
			in[k] = &mcp.ElicitResult{Action: "accept", Content: map[string]any{"confirm": true}}
		}
		p = &mcp.CallToolParamsRaw{Name: "t", Arguments: json.RawMessage(`{}`), RequestState: ir.RequestState, InputResponses: in}
	}
	t.Error("the gateway kept asking")
}
```
