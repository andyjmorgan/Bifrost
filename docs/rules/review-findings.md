# Review findings: the rule language as a tree

Written November 17, 2026, by an independent reviewer. Branch `rules-hooks`, working tree as it stood that day, uncommitted changes included.

## Verdict

The tree itself holds up. The two sides are right on every hook I tried, for tools, prompts and resources, before and after a continuation round. A rule that cannot be evaluated denies on every enforce hook I tried. Nothing outside a hook's own branch can be set, a read result cannot bring in an entry, and a manifest file keeps its bytes. The problems are around the tree, not in it. A deny written against a URI is passed by writing the scheme in capitals. A confirmation can be used more than once when the server asks its own question, and it does not cover an argument a rule set. A `set` key with a backslash in its branch is accepted and then does nothing. In the text form a `//` comment swallows the lines that continue the expression. There are also several places where the tree describes a document the rule does not get. I found 1 critical, 2 high, 6 medium and 12 low findings.

## How to run the tests

- Tests that pass: `go test ./pkg/hooks/ -run 'TestReview' -count=1`
- Tests that fail today: `HOOKS_KNOWN_GAPS=1 go test ./pkg/hooks/ -run 'TestReviewGap' -count=1`
- Golden-form cases that fail today: `go build -o bin/hookctl ./cmd/hookctl && ./bin/hookctl run -summary pkg/hooks/testdata/known-gaps/98-tree.json`

All tests are in `pkg/hooks/review_tree_test.go`. The golden cases are in `pkg/hooks/testdata/known-gaps/98-tree.json`. Without the environment variable the gap tests skip. I ran `go test ./pkg/hooks/ ./internal/ruletext/ -count=1` once after adding them and both packages passed. I did not run the suite under `-race`.

The handover names no test file for `internal/ruletext`, so the text-layer findings (19 and 20) have no kept test. I reproduced them with a scratch test that I deleted. Finding 19 gives a test body ready to paste.

## Findings

### 1. A confirmation can be used again when the server asks its own question

Severity: high.

Rule, on `tools/call.pre`:

```
when: !has(request.confirmations.sure)
then: confirm: elicit('sure', 'Really?')
```

Request:

1. Call `t` with `{"q":"x"}`. The gateway asks.
2. Send the answer with the gateway's request state. The call is forwarded.
3. The server replies `input_required` with its own question. The gateway returns a signed envelope as the request state.
4. Send that envelope again with the same arguments, four times: three with the server's answer, one with no input responses.

What happened: all four were forwarded. The envelope carries the confirmation in its `Answers`, and nothing spends it. It is good for five minutes, for the same principal and the same arguments.

What the specification says: confirmation state is bound to the principal and the arguments. The handover asks for a confirmation that cannot be replayed. The gateway's own challenge is taken from the store once, and case CX-54 holds it to that. The envelope around a server's question has no such limit.

Why not critical: the caller and the arguments are the ones the user confirmed. What is lost is single use.

Test: `TestReviewGapConfirmationReplayedThroughAServersQuestion`. I ran this for `tools/call`. `prompts/get` and `resources/read` use the same `continuation` and `wrapState` code; that they behave the same is inferred.

### 2. A confirmation does not cover an argument a rule set

Severity: high.

Rules, on `tools/call.pre`, in this order:

```
when: true
then: request.params.arguments.env: request.params.?_meta.?env.orValue('staging')

when: !has(request.confirmations.sure)
then: confirm: elicit('sure', 'Deploy to {env}?', {'env': request.params.arguments.env})
```

Request: call `deploy` with `{}` and `_meta` `{"env":"staging"}`. The user is asked `Deploy to "staging"?` and accepts. The client sends the answer with `_meta` `{"env":"production"}`.

What happened: the call was forwarded with `{"env":"production"}`.

Why: the state holds a hash of the arguments as the client sent them, taken before any call rule runs (`engine.go`, `hash := argsHash(args)`). The comment in `runIf` says the challenge binds the arguments as patched so far. It does not. Anything a rule reads outside the arguments can differ between the question and the answer: `request.params._meta`, `request.headers`, `request.client`, `request.flags`, `request.time`.

What the specification says: state is bound to the principal and the arguments.

Test: `TestReviewGapConfirmationCoversArgumentsARuleSet`. I ran it with `_meta`. The same with a header is inferred.

### 3. A scheme written in capitals passes a deny

Severity: critical, under the conditions stated. This is older than the tree rewrite: `visibility.go` and `uris.go` changed by one line in this work.

Rules:

```
resources/list.post            when: resource.uri.startsWith('file:///secrets/')            then: decision: 'deny'
resources/templates/list.post  when: template.uriTemplate.startsWith('file:///vault/')      then: decision: 'deny'
```

Catalog: a template `file:///vault/{name}`. The server does not list `file:///secrets/key`.

Requests: `resources/read` of `FILE:///secrets/key`, `File:///secrets/key` and `FILE:///vault/a`.

What happened: all three were forwarded, with the URI as typed. The lower-case forms were refused.

Why: the engine lower-cases the scheme when it compares a URI with a listed resource or a skill folder (`normalizeURI`). A rule reads `resource.uri` as the client typed it, and a denied template is matched against the typed text.

Conditions: the URI is one the server does not list, or one only a denied template covers. The server reads the scheme without regard to case. I did not test a real MCP server. Go's `net/url` reads `FILE:///secrets/key` with the scheme `file`; the test checks that. That other servers do the same is inferred.

What the specification says: to close a folder, deny its prefix. A denied template denies the URIs it can produce. A URI that is not in canonical form is refused.

Related, inferred and not run as a finding: the host is never lower-cased, so `docs://Host/x` is a different URI from `docs://host/x` to the gateway. A fragment or a query makes a new URI too. In a scratch run a server's entry for `docs://hidden#f` was returned although `docs://hidden` was hidden by an exact-match rule.

Tests: `TestReviewGapSchemeInCapitalsPassesADeny`, and the two golden cases named RT-03.

### 4. A `set` key with an escape in its branch is accepted and does nothing

Severity: medium. The text form cannot write such a key, so this needs a rule in the engine's JSON form.

Rule, on `tools/call.post.result`:

```
when: true
then: "respons\e.result.content": [{'type': 'text', 'text': 'REDACTED'}]
```

Request: call `t`. The server returns `{"content":[{"type":"text","text":"SECRET"}]}`.

What happened: the rule compiled. The client got `SECRET`, and a second key named `respons\e.result.content` beside it. The hit says `set`.

Why: `HookSpec.field` splits the key with backslash escapes before it walks the tree. `runIf` takes the hook's branch off the key with a plain `strings.TrimPrefix`. The two disagree, and the setter is handed the whole key.

The same on the other hooks I tried:

- `server.settings`, `"setting\s.tools": false`: tools stayed listed.
- `tools/call.pre`, `"reques\t.params.arguments.q": 'pinned'`: the server got the client's value.
- `tools/list.post`, `"to\ol.name": 'renamed'`: the tool kept its name.
- `tools/call.post.elicit`, `"elicitatio\n.params.message": 'SAFE'`: the server's message went out unchanged.

What the specification says: `set` uses the same paths as reads. A path that cannot be changed is refused when the rule is saved.

Tests: `TestReviewGapEscapedSetKey`, golden case RT-04.

### 5. A set of an annotation MCP does not name is lost

Severity: medium.

Rules:

```
tools/list.post   when: true   then: tool.annotations.risk: 'high'
tools/list.post   when: true   then: tool.description: 'risk=' + tool.annotations.risk
tools/call.pre    when: cel.bind(a, tool.?annotations.orValue({}), a.?risk.orValue('low')) == 'high'   then: decision: 'deny'
```

Request: `tools/list`, then call `t`.

What happened: the second list rule saw it (`risk=high`). The listing sent to the client has no `risk`. The call was forwarded: the call hook does not see it. The annotations go through the SDK's `ToolAnnotations` type, which has no place for other keys. On the call hooks a direct read of `tool.annotations.risk` is refused when saved, because there the map is closed.

What the specification says: the tree marks `tool.annotations.*` writable on the list hook. A set should take effect where the path says, or be refused.

Also seen here: setting any annotation on a tool that had none puts `readOnlyHint: false` and `idempotentHint: false` on the wire. That follows from the known limit on those two hints.

Test: `TestReviewGapUnknownAnnotationIsDropped`.

### 6. A zero the server sent is absent

Severity: medium.

Catalog, as the server sent it: a resource with `"annotations":{"priority":0}`, and a prompt argument with `"required":false`.

Rule, on `resources/list.post`:

```
when: resource.?annotations.?priority.orValue(1.0) < 0.5
then: decision: 'deny'
```

What happened: the resource was listed. `priority` read as absent, the rule's default applied, and the wire form lost the field too (`"annotations":{}`). The prompt argument's `required` read as absent.

Why: the catalog is held in the SDK's types, which drop a zero. The handover lists the opposite case for `readOnlyHint` and `idempotentHint`, which are filled in. This one leaves a value out, and it is not in the list of known limits.

What the specification says: nothing is filled in, and an optional field the server left out is absent. Here a field the server sent is absent.

Smaller, read and not run: `Resource.name` and `ResourceTemplate.name` are not `omitempty` in the SDK, so a resource with no name has `name: ""`. The tree marks `name` optional.

Test: `TestReviewGapAZeroTheServerSentIsAbsent`.

### 7. The tree and the document disagree on the reply hooks

Severity: medium.

(a) `response.result.content` is marked always present. Rule on `tools/call.post.result`: `when: response.result.content.exists(c, c.type == 'image')`, `then: decision: 'deny'`. Server reply: `{"structuredContent":{"a":1}}`. The rule failed with `no such key: content` and the result was refused. No note was given when the rule was saved.

(b) The result a rule reads holds every key the server sent. The tree has four, and the schema says no others. A reply with `"resultType":"complete"` does not validate against `hookctl schema tools/call.post.result`. A direct read of `response.result.resultType` is refused when saved; through `cel.bind` it works. In a scratch run the same was true of an entry of `contents` that carried `annotations`, and of a tool whose `inputSchema` had a boolean property schema.

(c) `elicitation.upstream` is described with `key`, `method` and `params`, all always present. The document has `params` only. `when: elicitation.upstream.method == 'elicitation/create'` compiled with no note, failed on a real question and refused it.

(d) On the `resources/read` hooks `request.method` is `resources/directory/read` for a directory read. The tree's description says `resources/read`. Seen in a scratch run.

(e) On the mux `request.params.name` and `request.params.uri` carry no slug. The client sent `a+docs://a` and the rule read `docs://a`. The specification says a branch that is a message looks exactly like the message on the wire. `TestReviewBothSidesOnEveryResourceHook` shows the value.

What the specification says: the tree for a hook is the document a rule reads, and a document built for a hook validates against its schema.

Tests: `TestReviewGapResultTreeAndDocument` for (a), (b) and (c). Golden case RT-07 for (a).

### 8. A macro variable with the name of a branch is checked as the branch

Severity: low. A valid rule is refused. I found no case where a rule that should be refused compiles this way.

Rules refused when saved:

```
resources/read.post.result   response.result.contents.exists(resource, resource.text.contains('x'))
tools/call.post.result       response.result.content.filter(tool, tool.type == 'text').size() > 0
tools/call.pre               [{'nosuch': 1}].exists(request, request.nosuch == 1)
```

The message is `resource has no "text"`. `resource` and `tool` are natural names for a loop variable on these hooks.

Tests: `TestReviewGapMacroVariableNamedLikeABranch`, golden case RT-08.

### 9. The note on an unguarded optional read misses the common forms and fires on guarded ones

Severity: low.

Missed, no note:

- `response.result.content.exists(c, c.text.contains('secret'))`. A result with an image block and no match fails the rule and is refused. This is the form the docs recommend for lists. `response.result.content[0].text` is noted.
- The same on `response.result.contents`.
- `request.headers['x-team'] == 'sre'`. A request without the header fails the rule.
- `request.confirmations.sure.action == 'accept'`.
- `when: !has(tool.title)` with `then: tool.description: tool.title`. Any `has()` in the condition hides the note on the value, whatever its sense.

Fired on a guarded read:

- `has(tool.title) && tool.title == 'x'`. The note tells the author to guard with `has()`.
- `tool.?annotations.destructiveHint.orValue(true)`. CEL treats the step after `.?` as optional. In a scratch run this read gave `true`, `true` and `false` for a tool with no annotations, empty annotations and the hint set to false.

Reads through `cel.bind` or a macro variable are not walked at all, so a wrong key there is found only when a request arrives. Those fail closed; see the sound list.

Test: `TestReviewGapNotesMissAndMisfire`.

### 10. A constant of the wrong type is saved

Severity: low. It fails closed when a request arrives, but it takes the whole hook with it.

Saved without complaint: `settings.tools: 'off'`, `timeout: '30s'`, `tool.annotations.readOnlyHint: 'yes'`, `tool.name: 5`, `request.params.arguments.tone: 5` on `prompts/get.pre`, `response.result.content: 'text'`.

What happened at request time: with `settings.prompts: 'off'` every switch went off and no tool was listed. With the others the tool was hidden or the call refused. `compileExpr` is given no wanted type for a `then` value.

Tests: `TestReviewGapWrongTypedConstantIsSaved`, golden case RT-10. `TestReviewAWrongTypedSetFailsClosed` passes and shows the failure is closed.

### 11. A change to a file under a skill folder that the manifest does not list is lost

Severity: medium.

Catalog: a skill `skill://demo/SKILL.md` whose manifest lists only `SKILL.md`.

Rule, on `resources/read.post.result`:

```
when: true
then: response.result.contents: response.result.contents.map(c, {'uri': c.uri, 'text': c.text.replace('hunter2', '[redacted]')})
```

Request: read `skill://demo/notes.md`. The server returns `the password is hunter2`.

What happened: the client got `the password is hunter2`. The hit says `set`. No issue was recorded. `UpstreamReadReply` puts back the bytes of every entry under a visible skill's folder (`skillFor`), not only of the files in the manifest.

What the specification says: a file a skill's manifest vouches for keeps its bytes whatever a rule sets. This file is not in the manifest.

Test: `TestReviewGapChangeToAFileUnderASkillFolderIsLost`.

### 12. `response.upstream` of a rewritten skill file holds the rewrite

Severity: low.

With a static rule that rewrites `SKILL.md` (`server body` to `gateway body`), the gateway applies it ahead of time and then runs the result hook on the rewritten entry. A second rule that copies `response.upstream.result.contents[0].text` got `gateway body`.

What the specification says: `response.upstream` is the server's response, unchanged.

Test: `TestReviewGapResponseUpstreamOfASkillFile`.

### 13. An internal variable and function can be named by a rule

Severity: low.

On `resources/read.post.result` these compile: `__mux.slug == 'a'`, `size(__mux.owned.exact) > 0`, and `__slugUris('see docs://x', {...})` with a second argument the rule chooses. Neither name is in the tree. `__mux.owned` lists the URIs this entitlement shows, so I saw no leak across entitlements.

Test: `TestReviewGapInternalVariableIsReadable`.

### 14. A deny that reads what an earlier list rule set misses an unlisted URI

Severity: low. The behaviour is documented; this is the trap in it.

Rules, on `resources/list.post`:

```
when: resource.uri.startsWith('file:///private/')         then: resource._meta.private: true
when: resource.?_meta.?private.orValue(false)             then: decision: 'deny'
```

Requests: read `file:///private/listed`, which the server lists, and `file:///private/unlisted`, which it does not.

What happened: the first was refused, the second forwarded. On an unlisted URI only rules that deny are run (`runIf` with `keep`), so the mark is never set and the second rule's own default applies. With a plain read in place of `.?` the rule fails and the read is refused.

Test: `TestReviewGapMarkThenDenyOnAnUnlistedRead`.

### 15. An absent container reads as null

Severity: low.

- A tool with no `inputSchema`: `tool.inputSchema` is present with the value null. `tool.upstream.inputSchema` is absent. `toolState.resolve` returns the worked-out schema with `true` whatever it is.
- A skill with no frontmatter: `skill.frontmatter` is present as null. `item.resolve` tests `v != nil` on a nil map held in an interface, which is not nil.

`has()` is true for both. A read below either still fails, so this fails closed.

What the specification says: an explicit null is treated as absent, and nothing is filled in.

Test: `TestReviewGapAbsentContainerReadsAsNull`.

### 16. A set of a list is not checked below the list

Severity: low.

- `tools/call.post.result`, `response.result.content: [1, 'two', {'no': 'type'}]`: sent to the client as written.
- `resources/read.post.result`, `response.result.contents: [{'uri': 'docs://a', 'text': 5, 'blob': [1]}]`: sent as written.
- In a scratch run `response.result.structuredContent: 'a string'` was sent as written.
- In a scratch run a read rule set a URI of `docs://h/x/../a` for a returned `docs://h/a`. It was accepted and sent. The gateway refuses that form from clients and drops it from servers.
- In a scratch run on `tools/call.pre`, an argument set to `duration('1s')` reached the server as the number `1000000000`, and a map with the keys `1` and `true` as `{"1":…,"true":…}`.

Test: `TestReviewGapMalformedContentIsAccepted`, for the first two.

### 17. An engine with no signing key accepts a confirmation the client wrote

Severity: low. The gateway in this repository always sets a key: `newEngine` makes a random one and `SetStateKey` ignores an empty one. I read both.

`(&Engine{}).CallTool` with a request state sealed by `Signer{}` and `Answers` holding `sure` was forwarded. The engine is meant to be lifted into another gateway as it is. It does not refuse to seal or open state with an empty key.

Test: `TestReviewGapEngineWithNoSigningKey`.

### 18. Retired names remain in messages

Severity: low.

- A client can be told `the rule asking for it must check has(input.sure)` (`engine.go`, `elicitation`). The name is now `request.confirmations`. Test: `TestReviewGapRetiredNameInAClientMessage`.
- Read, not run: the text layer's error says `confirm "Delete {args.id}?"` (`ruletext.go`). Copying it gives a rule that does not compile. The helper docs in `cel.go` say `drop(result.structuredContent, …)`, `dropSchema(outputSchema, …)` and `{"id": args.id}`.

### 19. In the text form a `//` comment swallows the lines that continue the expression

Severity: medium.

Text, on `tools/list.post`:

```
when tool.name == 'a' // or the other one
  || tool.name == 'b'
  deny
```

What happened: it compiled, with no problem and no warning, to `when: tool.name == 'a' // or the other one || tool.name == 'b'`. CEL reads a `//` comment to the end of the line, and `parseHook` joins continuation lines with a space. Tool `b` is not hidden.

The same in a value:

```
when tool.name == 'a'
  set request.params.arguments.limit = 5 // cap
    + 100
```

compiled to `5 // cap + 100`, which is 5.

A comment line that starts with `#` between the lines is handled correctly. A `//` comment on a `when` line with `confirm` is refused, because the guard is added after it.

Tests: `TestReviewACELCommentRunsToTheEndOfTheExpression` passes and shows the engine's half. For the text half, this body goes in `internal/ruletext`; I ran it as a scratch test and it fails today:

```go
func TestACommentDoesNotSwallowAContinuationLine(t *testing.T) {
	rules, probs := Compile(map[string]string{"tools/list.post": "when tool.name == 'a' // or the other one\n  || tool.name == 'b'\n  deny\n"})
	if len(probs) == 0 && strings.Contains(rules[0].When, "// or the other one ||") {
		t.Errorf("compiled to %q", rules[0].When)
	}
}
```

### 20. Smaller things in the text form

Severity: low. Scratch runs, no kept test.

- `confirm "a?"` and `deny "no"` in one rule compile to one engine rule with `confirm`, `decision` and `reason`. In `runIf` the deny wins, so the question is never asked.
- Two rules on one hook with the same question text share a confirmation key, which is a hash of the hook and the text. One answer satisfies both.
- A `{…}` in a question is cut at the first `}`, with no regard for strings. `confirm "Run {request.params.arguments['}']}?"` is refused with a CEL syntax error.
- A `set` path cannot hold a backslash, so an argument whose name has a dot cannot be set from text. The message is `this rule does nothing: add deny, confirm, set`.

### 21. Things the gateway fills in

Severity: low. Read, not run.

- `callerMap` in `internal/gateway/entitle.go` always gives `email`, `name` and `keyName`, as empty strings when there is none. A rule that reads `request.caller.keyName` for a user gets `''`, not a failure.
- `Context.roots` always gives `request.client.capabilities` as a map, and the tree marks it always present. The specification says `request.params.arguments` is the one container the gateway supplies.
- `neverRuleHeaders` is a fixed list of five names. A header such as `x-auth-token` or `x-amz-security-token` is shown if an operator puts it on the allow-list. The list starts empty, so this needs the operator to do it.

## Checked and found sound

- Both sides for tools after a rename of the tool and a parameter, with a default, a hidden pin and an argument added by a pre rule: `tools/list.post`, `tools/call.pre`, progress, elicit and result hooks, on the first round and after the server's own question. `TestReviewBothSidesOnEveryToolHookAfterAServersQuestion`.
- Both sides for prompts after a rename of the prompt and an argument: list, pre, progress and elicit hooks, before and after a continuation. `TestReviewBothSidesOnEveryPromptHook`.
- Both sides for resources after a list rule changed the title: list, pre, progress, elicit and result hooks, on the direct route and the mux. `TestReviewBothSidesOnEveryResourceHook`.
- `upstream` never showed a rule's change in those three tests, for a name, a description, a title, arguments and a result. The one exception is finding 12.
- A later rule saw an earlier rule's change on the list, pre, progress and elicit hooks, in the same tests.
- A call by the server's name, or with the server's parameter name, is refused after a rename. Same tests.
- Paths the save-time check does not see still deny when the request arrives: a computed key, `cel.bind`, a macro over a list, a ternary, a map literal, a list literal. `TestReviewPathsTheSaveCheckMissesStillDeny`.
- A leading dot is walked like any read, and `request.flags` used whole is refused through `cel.bind`, a computed key and `.?`. Same test.
- 57 `set` keys outside a hook's branch, or inside it and not marked, are refused when saved: `upstream` on every branch, `request.caller`, `request.headers`, `request.confirmations`, the item on a call hook, whole open maps, a key two levels into `_meta`, list indexes, `*` and empty segments. `TestReviewOnlyTheHooksOwnBranchCanBeSet`.
- A read result rule cannot add the hidden entry, a new URI, an entry with no URI, an entry that is not a map, or a URI that is a number. The hidden entry is not under `response.upstream`. One entry a rule cannot evaluate refuses the whole read, in a condition and in a value. An audit rule changes nothing. `TestReviewAReadResultCannotBringInAnEntry`.
- A manifest file keeps its bytes against new text, a blob, the URI in capitals, a second entry and a dotted variant. A rule can still drop it. `TestReviewAManifestFileKeepsItsBytes`.
- A confirmation is not satisfied by input responses alone, a made-up key, the state with no answer, other arguments, another principal, a second use, five kinds of answer that are not a ticked accept, or an envelope signed with another key. `TestReviewAConfirmationNeedsItsOwnState`.
- A value of the wrong type fails closed on the settings, list, pre and result hooks. `TestReviewAWrongTypedSetFailsClosed`.
- Plain reads of absent values deny on `tools/call.pre`. Scratch run, 12 forms: `_meta`, `clientInfo.title`, a caller field, a header, a confirmation, `server.url`, `entitlement.id`, `inputSchema.required`, a parameter's description, `tool.upstream.annotations`, `tool.annotations == null` and `tool.title == null`.
- Audit rules never deny or change anything. I read `runIf` and ran it on a read result and on an unlisted URI.
- `request.headers`: I read `SetRuleHeaders` and `headersFor` and ran the existing `TestRuleHeadersStartEmptyAndNeverShowCredentials`. Names are lower-cased in the gateway and again in `Context.roots`.
- The text form: 44 scratch texts. No `set`, `confirm` or `deny` line carried a second action. Keywords inside strings and brackets were not split. A second `set` of the same path, a second `deny` and a second `confirm` were refused.
- The tree against the SDK: every JSON field of the SDK's `Tool`, `Prompt`, `PromptArgument`, `Resource`, `ResourceTemplate`, `Annotations` and `Icon` is in the tree. Scratch run.
- Documents against schemas: scratch run over the list hooks, the three server hooks, `prompts/get.pre`, `prompts/get.post.progress`, `resources/read.pre` and the tool hooks. The mismatches are in findings 7 and 15.

## Not covered

- The golden set over real HTTP and through the Go, Python and TypeScript SDKs. I used the engine and `hookctl` only.
- The full suite under `-race`.
- A real MCP server's handling of a scheme in capitals. Finding 3 rests on `net/url`.
- Confirmation replay on `prompts/get` and `resources/read`, and the atomicity of the gateway's `cacheStates.Take`.
- `resources/directory/read` replies, the mux fallback, search mode and tasks, beyond reading the code.
- Rule budgets, cost limits and panics. I did not attack them.
- The schema of every hook. See the list above for the ones I validated.
- `hookctl hooks` output against the doc pages, and the doc pages themselves, which were being rewritten.
- `ruletext.Lint` beyond reading it, and the saved-text corpus.
- The web editor, `cmd/_migrate`, and whether the rewritten golden cases kept their meaning.
- `internal/gateway/hooks.go` beyond `load`, `clientOf`, `newEngine` and `SetStateKey`.
