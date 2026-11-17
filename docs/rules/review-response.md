# Response to the review of the tree

Written November 17, 2026, in reply to `review-findings.md`. It says what was done about each of the 21 findings.

The Codex review named in the handover did not run: its sandbox failed before it read a file. One independent review ran, and this responds to it.

## Summary

- Fixed: 19 findings, each with a test that failed before the fix and passes now.
- Recorded and not changed: findings 6 and 14, and parts of 7, 16, 20 and 21. Each says why below.
- Two fixes changed the rule language. `request.confirmations` is gone: the engine decides whether a question has been answered. A URI has one spelling the gateway accepts.
- A second reviewer then verified the fixes and found 15 more problems. Twelve are fixed and three recorded: see "The verification pass".

## What was done about each finding

| No. | Finding | What was done |
| --- | --- | --- |
| 1 | A confirmation can be replayed through a server's own question | Fixed. All request state is kept in the gateway's store, and the client holds a key that works once. The signed envelope and the signer are removed, so there is one mechanism |
| 2 | A confirmation does not cover an argument a rule set | Fixed. The engine decides whether a question is answered, not the rule. A yes counts only for the question it answered: its words as shown and the arguments as they stood when it was asked. If either differs when the answer arrives, it is a new question and is asked. `request.confirmations` is removed from the tree |
| 3 | A scheme in capitals passes a deny | Fixed, and widened after the verification pass (V1): a URI has one spelling the gateway accepts |
| 4 | A `set` key with a backslash in its branch is accepted and does nothing | Fixed. A key must start with the hook's branch as written |
| 5 | An annotation MCP does not name can be set and is then lost | Fixed. Only the annotations MCP names can be set |
| 6 | A zero the server sent is absent | Recorded. The catalog reaches the engine in the SDK's types, which drop a zero. Fixing it means carrying the server's raw JSON, which is a separate change. `TestReviewGapAZeroTheServerSentIsAbsent` reproduces it and runs with `HOOKS_KNOWN_GAPS=1` |
| 7 | The tree and the document disagree on the reply hooks | Fixed in part. (a) MCP requires `content`, so the tree keeps it as always there; a result without it fails a rule that reads it, and the docs say so. (b) `resultType` is in the tree. A key MCP does not name at the top of a result still cannot be read by name. (c) `elicitation.upstream` has `params` only. (d) `request.method` is described as it is, and a directory read now has `request.upstream.params.uri`. (e) Recorded: on the mux a rule reads a name or URI without the slug |
| 8 | A macro variable with the name of a branch is checked as the branch | Fixed. The path check follows what each macro binds |
| 9 | The unguarded-read note misses and misfires | Fixed. Reads through a macro variable or `cel.bind` are walked, a step after `.?` counts as guarded, `has(x) && x` is not noted, `!has(x)` does not guard, and a header is optional |
| 10 | A constant of the wrong type is saved | Fixed. A value whose type is known when the rule is saved and does not fit its path is refused then |
| 11 | A change to a file under a skill folder that the manifest does not list is lost | Fixed. Only `SKILL.md` and the files the manifest lists keep their bytes. A dynamic skill has no manifest, so all its files do |
| 12 | `response.upstream` of a rewritten skill file holds the rewrite | Fixed. `response.upstream` is the server's bytes |
| 13 | An internal variable and function can be named by a rule | Fixed. They have names a rule cannot write |
| 14 | A deny that reads what an earlier list rule set misses an unlisted URI | Recorded. On an unlisted read only the rules that deny are run, because there is no listing to change. `TestReviewAnUnlistedReadRunsOnlyTheRulesThatDeny` holds the behaviour |
| 15 | An absent container reads as null | Fixed |
| 16 | A set of a list is not checked below the list | Fixed for the forms MCP fixes: a content block is a map with a `type`, `structuredContent` is an object, an entry of `contents` keeps its `uri` exactly and has string `text`, `blob` and `mimeType`. Recorded: an argument set to a duration or to a map with keys that are not strings |
| 17 | An engine with no signing key accepts a confirmation the client wrote | Fixed with finding 1. There is no signing key |
| 18 | Retired names remain in messages | Fixed |
| 19 | A `//` comment swallows continuation lines in the text form | Fixed. A comment ends at the end of its line |
| 20 | Smaller things in the text form | Fixed: a rule that both denies and confirms is refused, and a brace inside a string in a question is text. By design: two rules with the same question share one answer. Recorded: a `set` path cannot name an argument that has a dot in its name |
| 21 | Things the gateway fills in | Fixed in part. A caller field the caller does not have is left out. A header whose name looks like a credential is never shown. Kept: `request.client.capabilities` is a map the gateway always supplies |

## Where the tests are

- `pkg/hooks/review_tree_test.go`: the reviewer's tests. The ones named `TestReviewFixed…` failed before the fixes.
- `pkg/hooks/testdata/scenarios/16-tree-review.json`: the reviewer's golden cases, with cases for findings 2 and 20.
- `internal/ruletext/ruletext_test.go`: findings 19 and 20.
- `internal/gateway/entitle_test.go`: finding 21.

## The verification pass

A second reviewer attacked the fixes and wrote `review-verification.md`: seven fixes were partial, and there were 15 new problems, one critical. This is what was done about each.

| No. | Problem | What was done |
| --- | --- | --- |
| V1 | Other spellings of a URI pass a deny (critical) | Fixed. A URI has one accepted spelling: no spaces, control characters or backslashes; the scheme in lower case; `//` where a URL parser would add it; for `file`, `http`, `https`, `ws`, `wss` and `ftp` the host in lower case with no user, trailing dot or default port; no dot or empty segments; percent escapes in upper case; no fragment. A hidden resource and a denied template also cover the URI with a query or a trailing slash added |
| V2 | An escape after the branch in a `set` key is accepted and does nothing | Fixed. A key must be its path exactly as written |
| V3 | A gateway question on the round that answers a server's question drops the server's round | Fixed. The request is refused and must be sent again |
| V4 | The same question twice with a set between can never be answered | Fixed. A yes is kept by what it answered, not by its key, so the second question is asked and answered |
| V5 | What a rule sets after a question is not covered | Recorded. The user was never shown that value, and a client that sends it on the first round gets the same result. The docs now say to ask after the sets and to show the values that matter |
| V6 | A file an outer skill's manifest lists is changed under a nested skill | Fixed. Every skill the file is under is asked |
| V7 | A constant of the wrong type is still saved | Fixed for null and an optional on a typed path. Recorded: a duration that is not positive, list elements of the wrong form, and a wrong value on a path that takes any. Each fails closed |
| V8 | An annotation MCP does not name is set through the whole map | Fixed. A map set on a path with fixed keys may have only those keys |
| V9 | A key a server sent on a read entry cannot be read by name | Fixed. An entry of `contents` is an open map |
| V10 | The unguarded-read note still misses and misfires | Fixed. A test guards only what runs when it holds: `has(x) && x`, `!has(x) \|\| x`, `has(x) ? x : y`, `x.hasValue() && x` |
| V11 | Wrong names are saved through forms the path check does not follow | Recorded. The docs now name the forms. Each fails when a request arrives, which denies |
| V12 | A read entry that is not an object is made into one | Fixed. It is dropped with an issue |
| V13 | A `//` inside a string that runs over lines is cut | Fixed. A string must close on the line it opens on |
| V14 | A manifest file with no held text is not rewritten, with no issue | Fixed. The bytes stay, and the gateway records an issue when a rule's change is set aside |
| V15 | An unknown escape in a question loses its backslash | Fixed. It is refused |

Also fixed from the same report: a number hashes alike however it is written (`1`, `1.0`, `1e0`), and a whole number too big for 64 bits keeps every digit.

Tests: `pkg/hooks/review_verify_test.go`. The ones named `TestReviewVerifyFixed…` failed before these fixes. Three stay behind `HOOKS_KNOWN_GAPS=1`, for V5, V7 and V11. The text-layer tests are in `internal/ruletext/ruletext_test.go`.

## Known limits after both passes

- A zero the server sent reads as absent (finding 6).
- On an unlisted read only the rules that deny are run (finding 14).
- What a rule sets after a question is not part of the question (V5).
- The path check does not follow a value through a ternary, a literal, `filter(...)[0]` or a computed key (V11).
- A constant whose form is wrong below its type is found when a request arrives (V7).
- On the mux a rule reads a name or a URI without the slug.
- No one has attacked rule budgets, panics, search mode or tasks in this round.
