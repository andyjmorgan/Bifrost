# The golden set

Every test case for the rules engine is a JSON object in this folder. Each case exists once, here, and nowhere in Go. Two runners read the same files:

- the engine harness: `hookctl run`, and `Scenario.Run` in `pkg/hooks/scenario.go`. It drives the engine alone, with no wire.
- the golden runner: `TestGolden` in `pkg/hooks/harness`. It takes every case over real HTTP, once per client harness, against a mock upstream or the real reference servers.

To add a case, add one JSON object to a file. To add a client, add one driver and one line (see "Adding a client").

## Folders

| Folder | What it holds |
| --- | --- |
| `scenarios/` | the common set: engine behaviour, one area per file |
| `doc-checks/` | the common set: one case per example in `docs/rules/hooks/` |
| `e2e/` | the common set: cases written against what a client sees |
| `e2e/outliers/` | outliers: cases that make sense on one harness only |
| `fuzz/` | not part of the golden set |

## Three tiers

1. The common set. A case runs on every harness. This is where a case belongs unless there is a reason.
2. Exclusions. A common-set case that one harness cannot run stays in the set, with an `exclude` entry that names the harness and says why.
3. Outliers. A case that only makes sense on one harness lives in `e2e/outliers/` and names that harness in `only`. A test that needs code lives in Go beside its harness, with a comment saying why it is an outlier.

## Harness names

| Name | What it is |
| --- | --- |
| `engine` | the engine alone, no wire (`hookctl`) |
| `raw` | a bare HTTP client |
| `go-sdk`, `python-sdk`, `typescript-sdk` | the official MCP client SDKs |
| `server:go`, `server:python`, `server:typescript` | the reference servers, one per official server SDK |

## A case

```json
{"name": "hide a tool by name",
 "servers": [{"slug": "a",
              "rules": [{"hook": "tools/list.post", "when": "tool.name == 'delete_page'", "then": {"decision": "'deny'"}}],
              "catalog": {"tools": [{"name": "echo", "inputSchema": {"type": "object"}},
                                    {"name": "delete_page", "inputSchema": {"type": "object"}}]}}],
 "steps": [
  {"op": "tools/list", "expect": [{"path": "client.result.tools[*].name", "equals": ["echo"]}]},
  {"op": "tools/call", "params": {"name": "delete_page", "arguments": {}},
   "expect": [{"path": "client.error.code", "equals": -32602}]}
 ]}
```

Case fields:

| Field | Meaning |
| --- | --- |
| `name` | unique within its file |
| `route` | `direct` (default) or `mux`. Search mode is the `search` flag, on either route |
| `flags` | The request's switches, for example `{"search": true}` or `{"cli": true}`. A flag can be set for either route. Rules read them as `request.flags.search` and `request.flags.cli` |
| `caller`, `client`, `now` | the request context. Rules read them as `request.caller`, `request.client` and `request.time`. `client` is `{clientInfo, capabilities}`. A step can override each |
| `limits`, `secret` | engine limits and the signing key |
| `servers` | the entitlements: `slug`, `rules`, and for a mock a `catalog` (and optionally `info`, `entitlement`, `generation`, `metaAllow`, `unavailable`) |
| `steps` | the requests, in order |
| `upstream` | `mock` (default) or `reference`. See "Upstreams" |
| `gateway` | gateway transport options: `token`, `keepAliveMs` |
| `exclude` | harnesses that must not run the case. See "Exclusions" |
| `only` | marks an outlier. See "Outliers" |

Step fields:

| Field | Meaning |
| --- | --- |
| `op` | the request: an MCP method (`tools/list`, `tools/call`, `prompts/get`, `resources/read`, and so on), or one of the engine operations `discover`, `connect`, `config`, `tool_search`, `list_tools`, `get_tool_definition`, `execute_tool`, `get_server_instructions` |
| `params` | the request's params, as written. `"${steps.N.path}"` is replaced by a value from step N's output |
| `upstream` | the result the mock upstream returns when the step is forwarded |
| `mock` | what the mock does besides returning that result. See "The mock upstream" |
| `progress` | `true`: the client asks for progress |
| `answers` | the client's scripted answers to the questions it is asked. See "Questions and answers" |
| `caller`, `client`, `now`, `slugs` | overrides for this step |
| `rules`, `generation` | a policy change that takes effect from this step on |
| `token` | the bearer token the client sends, when `gateway.token` is set |
| `rawBody` | an HTTP body sent as it is, in place of `op` and `params` (raw client only) |
| `exclude` | harnesses that must not make this step |
| `expect` | the assertions |

## Expectations

An expectation is a `path` and one or more operators.

```json
{"path": "client.result.tools[name=echo].title", "equals": "Echo"}
```

Paths: `a.b` for keys, `[2]` for an index, `[*]` to collect every element, `[field=value]` to pick the elements whose field has that value, and `\.` for a dot inside a key.

What a path can start with:

| Root | What it is | Harnesses |
| --- | --- | --- |
| `client.result`, `client.error` | the final message the client received. `client.error` is `{code, message}` | all |
| `progress` | the progress events the client received, in order | all |
| `hits` | every rule hit of the step: `{hook, rule, item, mode, effect}` | all |
| `outcome`, `reply` | the engine's outcome for the request, and for the upstream's reply. `outcome.forward` is what was sent upstream; over a wire it is what the mock upstream actually received | all |
| `problems`, `warnings` | rule compile problems, by slug | all |
| `questions` | the questions the client's elicitation handler was asked | clients |
| `upstream.requests` | every request the mock upstream received during the step | clients, mock only |
| `request.headers`, `request.meta` | the client's request as the gateway received it | clients |
| `sdk` | the client library: `name`, `version`, `protocolVersion` | clients |
| `gateway.rounds` | how many requests the client made for the step | clients |
| `wire` | the HTTP reply: `status`, `stream`, `headers`, `frames`, `messages`, `comments` | `raw` only |

Operators:

| Operator | Holds when |
| --- | --- |
| `equals`, `notEquals` | the value is, or is not, this JSON value. Numbers compare exactly |
| `exists` | the path is present (`true`) or absent (`false`) |
| `contains`, `notContains` | a string holds the text; a list holds the element; an object has the key |
| `len` | a list, object or string has this length |
| `matches`, `notMatches` | a regular expression matches the string, or the value's JSON |
| `keys` | an object has exactly these keys, in any order |
| `set` | a list has exactly these elements, in any order |
| `has` | a list holds an element with every field given: `{"path": "hits", "has": {"hook": "tools/call.pre", "effect": "deny"}}` |
| `hasText`, `lacksText` | the value's JSON holds, or does not hold, each text |
| `refused` | on the path `client`: the request was refused (a JSON-RPC error with a code, or a result with `isError: true`). A client that only failed, with no error code from the gateway, was not refused. `false` needs a result that is not an error, so a client failure passes neither `true` nor `false` |

A check needs something to check:

- `notEquals`, `notMatches` and `lacksText` fail when the path is missing. On a missing value they would pass whatever happened, including an error that returned no result
- to assert that a value is absent, use `"exists": false`
- `set` and `len` need a present value of the right kind
- a `[*]` path that collects nothing needs the list it collects from to be present. `tools[*].name` equal to `[]` holds for an empty listing and fails for an error with no listing

The loader refuses a key the format does not define, on a case, a server, a step, a check or a mock script. A misspelt key would otherwise be ignored and leave a case that tests nothing. Catalogs, arguments, replies and rules are data and may hold any key.

Expected values may use `${client.name}` (the name the client declares), `${client.harness}` and `${server.language}`.

## Upstreams

### The mock upstream

The default. Each server's `catalog` is served by a mock MCP server, and a forwarded step is answered with the step's `upstream` result (or a plain one). `mock` scripts the rest:

```json
{"op": "tools/call", "params": {"name": "t", "arguments": {}}, "progress": true,
 "mock": {"progress": [{"progress": 1, "total": 2, "message": "half"}, {"progress": 2, "total": 2, "message": "all"}]},
 "upstream": {"content": [{"type": "text", "text": "done"}]},
 "expect": [{"path": "progress[*].message", "equals": ["half", "all"]}]}
```

| `mock` field | Meaning |
| --- | --- |
| `progress` | progress events sent before the result, under the token the request carried; with no token they are not sent unless `forceProgress` is set |
| `notifications` | other notifications, `{method, params}`, sent after the progress |
| `noResponse` | the stream ends without a response |
| `error` | a JSON-RPC error instead of a result |
| `then` | the results of the second and later requests a step forwards, when a client makes several rounds |
| `stream` | answer with an SSE stream even with no events |
| `delayMs`, `gapMs` | wait before the first byte, and between events |
| `status`, `body` | a plain HTTP status and body |
| `rawBody` | these bytes in place of the JSON-RPC message |
| `hang` | keep the request open until the caller goes away |

The engine harness runs `progress`, `notifications`, `noResponse` and `error` through the engine's stream entry points. The others need a connection, so a case that uses them excludes `engine`.

### The reference servers

`"upstream": "reference"` runs the case against the real reference servers in `pkg/hooks/harness/servers` (their tools, prompts and resources are listed in its `SPEC.md`), once per server language. Servers then carry a slug and rules only. On the mux, the first server is the column's language and each further server is another language.

```json
{"name": "tools/list.post rename: rename a tool and call it by the new name",
 "upstream": "reference",
 "exclude": [{"harness": "engine", "reason": "This case drives the real reference servers (echo) over a wire; the engine harness has no connection to a real server."}],
 "servers": [{"slug": "ref", "rules": [{"hook": "tools/list.post", "when": "tool.upstream.name == 'echo'", "then": {"tool.name": "'say'"}}]}],
 "steps": [
  {"op": "tools/call", "params": {"name": "say", "arguments": {"text": "hi"}},
   "expect": [{"path": "client.result.content[0].text", "equals": "hi"}]}
 ]}
```

A real server records nothing. Assert what it received through its own replies: the `search` tool echoes its arguments and `inspect` reports the request as the server saw it.

## Questions and answers

`answers` scripts the client's elicitation handler. The client then makes every round of the request itself, as a real client does, and the expectations describe how the request ended:

```json
{"op": "tools/call", "params": {"name": "delete_page", "arguments": {"id": "42"}},
 "answers": [{"action": "accept", "content": {"confirm": true}}],
 "expect": [{"path": "client.result.content[0].text", "equals": "deleted 42"},
            {"path": "questions[0].message", "equals": "Delete page \"42\"?"}]}
```

The client must declare elicitation: `"client": {"capabilities": {"elicitation": {}}}`. A case with `answers` excludes `engine`, which has no client.

A step can also send `requestState` and `inputResponses` by hand in `params`, to replay or alter a round. Only the raw client can do that, so such a step excludes the SDK harnesses.

## Exclusions

A case is never dropped because a harness cannot run it. It stays, with an entry per harness and a reason that is specific to that case and that harness: what the harness does, and why that stops this case.

```json
"exclude": [
  {"harness": "typescript-sdk", "reason": "The TypeScript SDK does not send tools/list when the discover result did not advertise the tools capability. In this case discovery advertises none, so the request this step asserts on is never made."},
  {"harness": ["go-sdk", "python-sdk"], "reason": "..."}
]
```

- `exclude` goes on a case or on one step. `harness` is one name, or a list of names that share the reason.
- A case excluded for a harness does not run there.
- A step excluded for a client harness is made by the raw client in its place, so the steps after it still have what they need. A step excluded for `raw`, `engine` or a server harness is not made.
- The runners decide nothing themselves. A case or step that cannot run on a harness and has no entry fails, and says what entry is missing.
- `HOOKS_RUN_EXCLUDED=1` runs excluded cases and steps anyway and fails on any that pass, so a stale exclusion is found. In that mode only stale exclusions fail.
- `HARNESS_SUGGEST_EXCLUDES=1 go test ./pkg/hooks/harness/ -run TestGolden -v` prints a `SUGGEST` line for every missing entry, with what the harness could not do. The reason is still yours to write.

## Outliers

An outlier is for one harness. It lives in `e2e/outliers/`, names the harness and says why it is specific to it. It runs there and nowhere else, and needs no exclusions.

```json
{"name": "on the wire: a slow reply is preceded by comment lines that keep the stream alive",
 "only": {"harness": "raw", "reason": "It counts the SSE comment lines before the result; every SDK discards comment lines before its caller sees anything."},
 "gateway": {"keepAliveMs": 20},
 "servers": [{"slug": "a", "catalog": {"tools": [{"name": "t", "inputSchema": {"type": "object"}}]}}],
 "steps": [
  {"op": "tools/call", "params": {"name": "t", "arguments": {}}, "mock": {"delayMs": 250},
   "upstream": {"content": [{"type": "text", "text": "slow"}]},
   "expect": [{"path": "wire.frames[0].comment", "equals": true}, {"path": "wire.messages", "len": 1}]}
 ]}
```

## Running

```sh
# the engine harness
go build -o bin/hookctl ./cmd/hookctl
./bin/hookctl run -summary pkg/hooks/testdata/scenarios/*.json pkg/hooks/testdata/doc-checks/*.json \
    pkg/hooks/testdata/e2e/*.json pkg/hooks/testdata/e2e/outliers/*.json

# every client harness, the mock and the reference servers
./pkg/hooks/harness/clients/setup.sh   # once: the Python and TypeScript client SDKs
./pkg/hooks/harness/servers/setup.sh   # once: the Python and TypeScript reference servers
go test ./pkg/hooks/harness/ -run TestGolden -race -count=1 -v
```

`TestGolden` ends with one summary: cases down, client harnesses and upstreams across, then counts by harness and tier, the exclusions applied with each reason and its count, and the outliers. A client or server whose runtime is missing is reported by name; `MATRIX_CLIENTS_REQUIRE` and `REFSERVERS_REQUIRE` (`all`, or a comma-separated list) turn that into a failure.

## Adding a client

1. Write a driver for the SDK: a program that reads one JSON request per line and writes one JSON reply per line (see `pkg/hooks/harness/clients/driver.go` and the two drivers beside it).
2. Add one line to the `drivers` table in `pkg/hooks/harness/drivers_test.go`.

The new harness then runs the whole common set. Run with `HARNESS_SUGGEST_EXCLUDES=1` to see which cases it cannot run, and give each an `exclude` entry with its reason.

## Keeping cases portable

A case may not depend on Go types, Go helpers or Go names. Everything a runner needs is in the JSON: any language that can read these files, make the requests and evaluate the operators above can run the set.
