# Review handover: the rule language as a tree

Written November 17, 2026, for an independent review. Branch `rules-hooks`.

The rules engine's language was rebuilt in one change. Rules no longer read loose names such as `name`, `args` and `result`. They read one tree per hook. This handover says what changed, where the risk is, and how to report what you find.

## What to read first

- The design: the section "Follow-up: the rule language as a tree (settled November 17, 2026)" in the design note, `/home/localuser/vaults/Datadog/AI Platform/AI Gateway/MCP Gateway - Policy Engine Intent.md`. Its sub-sections "Principles", "The tree", "What changes in behaviour, not only in name" and "Decided during the build" are the specification. Earlier sections of the note use retired names.
- The tree itself: `pkg/hooks/tree.go`.
- The save-time path check: `pkg/hooks/paths.go`.
- The docs: `docs/rules/hooks/overview.md` and one page per hook beside it.

## What changed

| Area | Before | Now |
| --- | --- | --- |
| What a rule reads | flat names: `name`, `args`, `result`, `caller`, `upstream` | one tree: `tool`, `request`, `response`, `progress`, `elicitation`, `server`, `entitlement` |
| The item | fields at the top level, defaults filled in | `tool.name`, `tool.upstream.name`; an absent field is absent |
| The request | `args`, `input`, `caller`, `client`, `route`, `flags`, `now` | `request.params`, `request.confirmations`, `request.caller`, `request.client`, `request.headers`, `request.route`, `request.flags`, `request.time` |
| Arguments on post hooks | what the server received | `request.params.arguments` is the client's side, `request.upstream.params.arguments` the server's |
| A tool result | `result.content`, with an added `result.text` | `response.result.content`; `response.upstream` is the server's response, unchanged |
| A resource read result | the hook ran once per content item | the hook runs once over `response.result.contents` |
| `set` | short names: `name`, `args.limit`, `content` | the same paths as reads: `tool.name`, `request.params.arguments.limit`, `response.result.content` |
| Unknown names | refused only for top-level variables | every path is checked against the tree when the rule is saved |
| HTTP headers | not readable | `request.headers`, from an allow-list that starts empty |

## Where the risk is

These are the places a mistake would let something through, or refuse what should pass. Attack them first.

1. Fail closed on absent values. A plain read of an absent optional field must fail the rule, and a failed enforce rule must deny. Nothing may be filled in to make a read succeed. Look for any place the engine still fills a default, and any path where a failed rule lets the item or call through.
2. The two sides. `tool.name` and `request.params.name` are the client's names. `tool.upstream.name` and `request.upstream.params.name` are the server's. Look for a hook where they are swapped, stale after a rename, or where `upstream` reflects a rule's change.
3. Writable paths. Each hook may change one branch. Look for a path that can be set and should not be, a set that lands somewhere other than the path names, and a set on `upstream`, `request.caller` or another hook's branch that is accepted.
4. The resource read result hook. It now runs once over the whole response. A rule may change or drop an entry and may not add an entry for a URI the read did not return. Entries the caller may not read are removed before rules run. A file a skill's manifest vouches for keeps its bytes whatever a rule sets. Look for a way to return content the caller should not get, to alter a skill file, or to make one bad entry pass with the rest.
5. The save-time path check (`paths.go`). It walks member chains in the parsed expression. Look for a form of access it does not see: index expressions, optional forms, macro variables that shadow a branch, a leading dot, `cel.bind`. A path it misses is only checked when a request arrives.
6. `request.headers`. Credentials must never reach a rule. The gateway's allow-list is in `internal/gateway/gateway.go`.
7. Confirmations. The text form's `confirm` compiles to a check of `request.confirmations` and an `elicit(...)` call under the `confirm` key. State is bound to the principal and the arguments. Look for a confirmation that can be replayed, answered by another caller, or satisfied by client-supplied `inputResponses` alone.
8. Agreement between the tree and what a rule really sees. `JSONSchema` is generated from the tree. Look for a hook where the document a rule reads has something the tree does not describe, or lacks something the tree marks as always there.

## What is tested

- `pkg/hooks/testdata/scenarios/15-tree.json`: the edge cases from the design discussion, as golden cases. They also run through the Go, Python and TypeScript client SDKs.
- `pkg/hooks/tree_test.go` and `pkg/hooks/tree_request_test.go`: the tree's own properties, the request on each method, headers, MCP's field names against the SDK's types, and the document a rule reads against its JSON Schema.
- `internal/ruletext/testdata/saved.json`: rule text as it was saved, which must keep compiling.
- The earlier golden cases, all rewritten to the new names by a tool that parses each CEL expression.

Run them:

```
go test ./pkg/... ./internal/... ./cmd/... -race -count=1
go build -o bin/hookctl ./cmd/hookctl
./bin/hookctl run -summary pkg/hooks/testdata/scenarios/*.json pkg/hooks/testdata/doc-checks/*.json pkg/hooks/testdata/e2e/*.json pkg/hooks/testdata/e2e/outliers/*.json
./bin/hookctl hooks tools/call.post.result      # the tree of one hook
./bin/hookctl schema tools/call.post.result     # its JSON Schema
```

## Known limits

- The names in the tree are checked against the Go SDK's types (`go-sdk` v1.8.0), not against the text of the 2026-07-28 specification.
- The Go SDK types `readOnlyHint` and `idempotentHint` as plain booleans. A tool that has annotations always carries those two, as false when the server left them out.
- `server.upstream` holds `title`, `description` and `instructions`. It does not hold `serverInfo`.
- The rewritten golden cases mostly keep their earlier expectations. A case whose rule read an optional field plainly was rewritten to the optional form, so those cases no longer prove what happens on a plain read. `TREE-07` does.
- There is no result hook for `prompts/get`, and no rule sees a JSON-RPC error from a server.

## How to report

- Do not change engine code, fixtures that pass, or docs. Add tests only.
- Write each finding to `docs/rules/review-findings.md`: a title, a severity (critical when stated conditions give an authorisation bypass; high, medium, low otherwise), the exact rule and request that show it, what happened, and what the design says should happen.
- For each finding you can reproduce, add a test to `pkg/hooks/review_tree_test.go` (engine) or a case to `pkg/hooks/testdata/known-gaps/` (golden form). A test that fails today must be skipped unless `HOOKS_KNOWN_GAPS=1` is set, so the suite stays green. Say in the finding which test reproduces it.
- Say what you checked and found sound, in one line each, so a reader can tell what was covered.
- Say what you did not get to.
