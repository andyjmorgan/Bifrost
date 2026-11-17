# Response to the review of the tree

Written November 17, 2026, in reply to `review-findings.md`. It says what was done about each of the 21 findings.

The Codex review named in the handover did not run: its sandbox failed before it read a file. One independent review ran, and this responds to it.

## Summary

- Fixed: 19 findings, each with a test that failed before the fix and passes now.
- Recorded and not changed: findings 6 and 14, and parts of 7, 16, 20 and 21. Each says why below.
- Two fixes changed the rule language. `request.confirmations` is gone: the engine decides whether a question has been answered. A URI with a scheme that is not lower case is refused.

## What was done about each finding

| No. | Finding | What was done |
| --- | --- | --- |
| 1 | A confirmation can be replayed through a server's own question | Fixed. All request state is kept in the gateway's store, and the client holds a key that works once. The signed envelope and the signer are removed, so there is one mechanism |
| 2 | A confirmation does not cover an argument a rule set | Fixed. The engine decides whether a question is answered, not the rule. A confirmation covers the question as shown and the arguments as they stood when it was asked. If either differs when the answer arrives, the request is refused. `request.confirmations` is removed from the tree |
| 3 | A scheme in capitals passes a deny | Fixed. A URI whose scheme is not lower case is refused, like any other URI that is not in canonical form |
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
