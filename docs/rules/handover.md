# Rules engine: handover

This page is for the engineers who take the rules engine into another gateway. It says what the engine is, what to take, what your gateway must supply, and what is known not to work yet.

## What it is

The rules engine decides what an MCP client may see and do through a gateway. An entitlement is one caller's view of one MCP server. Each entitlement carries rules, and the engine runs them at fixed points in a request, called hooks: when tools are listed, before a call is forwarded, when a result comes back, and so on. There are 19 hooks.

A rule is a condition and what to do when it holds. Both are CEL expressions. A rule can hide an item, refuse a call, ask the user to confirm, or change one thing the hook lets it change, such as a tool's name or a field of a result.

The engine does no I/O. Your gateway hands it a request and the server's catalog. It answers the request, or says what to forward. Your gateway forwards, and hands the reply back.

## Where to start reading

| To learn | Read |
| --- | --- |
| The rule language and how hooks run | [The overview](hooks/overview.md) |
| What one hook can read and change | [Its page](hooks/overview.md#hooks) |
| How Bifrost wired the engine in | [The wiring page](bifrost-wiring.md) |
| What is tested, case by case | [The test catalogue](test-cases.md) |
| Why the design is as it is | The design note, "MCP Gateway - Policy Engine Intent" |

## What to take

| Path | What it is | Notes |
| --- | --- | --- |
| `pkg/hooks` | The engine | Take it whole. It depends on `cel-go`, the MCP Go SDK and a YAML parser |
| `internal/ruletext` | The text form of rules, and what the editor needs to know about each hook | Take it whole. It compiles text to the engine's rules |
| `cmd/hookctl` | A command-line tool: run scenario files, check rules, print a hook's tree or its JSON Schema | Take it whole |
| `pkg/hooks/harness` | A test gateway and scripted MCP servers, used to run the scenario files over real HTTP through the Go, Python and TypeScript client SDKs | Take it if you want the same proof for your gateway |
| `internal/gateway/hooks.go` and `entitle.go` | How Bifrost calls the engine | A worked example, not a library. Your gateway will have its own |
| `web/src/components/entitlements` | The rule editor, with autocomplete that walks each hook's tree | Optional |

## What your gateway must supply

- The rules, compiled. Stored rule text goes through `ruletext.Compile`, then `hooks.Compile`. Keep the result until the entitlement changes. A set that does not compile leaves the entitlement exposing nothing.
- A generation string for each entitlement that changes whenever the entitlement or the server's catalog changes. The engine adds a fingerprint of the rules itself. A request that is waiting for an answer is tied to both, so a change ends it.
- The server's catalog, in the MCP Go SDK's types, already cut down to what the entitlement selects.
- A `Request` for each MCP request: the route, the caller, a principal, the client's `clientInfo` and capabilities, the time, and the servers on the route.
  - The principal is one string that identifies who is asking. Confirmations are bound to it. With no principal the engine refuses to ask for confirmation.
  - The caller is a map your gateway defines. Rules read it as `request.caller`. Leave out a field the caller does not have; do not fill it with an empty string.
  - Headers reach rules only through an allow-list that starts empty. Never put a header that carries a credential on it.
- A `StateStore` shared by every instance of the gateway. It holds the state of a request that is waiting for the user's answer. `Take` must return a state and delete it in one step. `MemoryStates` is for tests and a single instance only.
- `OnFault` and `OnUnknown`, if you want one place to log the engine's own failures and the shapes it did not recognise.
- `Limits`, if the defaults do not suit: two seconds of rule time for each request, 100 milliseconds for audit rules, 10 MiB for a server's reply.

## How a request goes through

1. Build the `Request` and its `Server`s.
2. Call the engine method for the MCP method: `ListTools`, `CallTool`, `GetPrompt`, `ReadResource` and the rest.
3. The `Outcome` holds a result to return, an error to return, or a `Forward` to send to the server.
4. After a forward, give the server's reply to the matching method with the `CallContext` from step 3: `UpstreamToolReply`, or `ToolReplyStream` for a stream. The `Outcome` holds what the client gets.
5. Record `Outcome.Hits`, the rules that matched, in your audit log. Record `Outcome.Issues` where an operator will see them.

## Decisions to know before you change anything

- Nothing is filled in. An optional field the server left out is absent, and a rule that reads it plainly fails. A rule that fails in enforce mode denies. Rules read optional values with CEL's optional forms.
- A rule cannot be evaluated: the item is hidden or the request is refused. The engine never lets something through because a rule broke.
- Each hook changes one branch of its tree. `X` is the entitlement's form of a thing and `X.upstream` is the server's. Nothing can set `upstream`.
- Every path a rule reads or sets is checked against the hook's tree when the rule is saved.
- A URI has one spelling the gateway accepts. Rules compare text, and a server may read several spellings as one URI, so the rest are refused. See [URI form](hooks/overview.md#uri-form).
- The engine decides whether a confirmation has been given, not the rule. A yes counts only for the question it answered: its words and the arguments at that moment.
- The state of a waiting request stays in the gateway. The client holds a key that works once.
- A read of a URI the server does not list is allowed unless a rule denies it. To close a folder, deny its prefix.

## Known limits

- A zero the server sent reads as absent. `priority: 0` on a resource, and `required: false` on a prompt argument, do not reach rules, because the catalog arrives in the SDK's types, which drop a zero. Fixing it means carrying the server's raw JSON.
- On a read of an unlisted URI, only the list rules that deny are run. A deny that depends on a mark an earlier rule set does not see the mark there. Write the deny against the URI.
- What a rule sets after a confirm question is not part of that question. Put the question after the rules that set arguments.
- The save-time path check does not follow a value through a ternary, a list or map literal, `filter(...)[0]`, or a key worked out when the rule runs. A wrong name there is found when a request arrives, and denies.
- A constant whose form is wrong below its type, such as `duration('0s')` for a timeout, is found when a request arrives.
- On the mux route a rule reads a tool name or a URI without the slug prefix.
- On the mux route, while one server's catalog is unavailable, a prefixed URI that the named server does not list is refused. The gateway cannot tell whether it belongs to the server that is down.
- A resource whose URI the gateway cannot put in canonical form, such as one with a user name in it, is not listed. Two resources the server lists under spellings of one URI are both hidden.
- There is no result hook for `prompts/get`, and no rule sees a JSON-RPC error from a server.
- Skills and directory reads are in the engine and its tests. Bifrost's front door does not serve them yet.

Tests for the limits that are bugs are in `pkg/hooks/known_limits_test.go`. They are skipped unless `HOOKS_KNOWN_GAPS=1` is set, and fail when it is.

## What has not been built

- Webhooks. Each hook's tree serialises to a JSON Schema (`hookctl schema <hook>`), which is meant to become the contract for one.
- Labels on rules, layering of rules across entitlements, and rate limits.

## How it was tested

- 685 scenario cases run through the engine, and again over real HTTP through the Go, Python and TypeScript client SDKs.
- Every example rule in the hook pages is run and checked against what its page says.
- Three independent reviews attacked the engine after the last redesign. They made 45 findings between them. Each is fixed, with a test that failed first, or is one of the limits and decisions on this page. The reports are in the repository's history, last at commit `f5b0034`, under `docs/rules/`.

Run everything:

```sh
go test ./pkg/... ./internal/... ./cmd/... -race -count=1
go build -o bin/hookctl ./cmd/hookctl
./bin/hookctl run -summary pkg/hooks/testdata/scenarios/*.json pkg/hooks/testdata/doc-checks/*.json pkg/hooks/testdata/e2e/*.json pkg/hooks/testdata/e2e/outliers/*.json
golangci-lint run ./pkg/hooks/... ./internal/ruletext/... ./internal/gateway/... ./cmd/hookctl/...
```
