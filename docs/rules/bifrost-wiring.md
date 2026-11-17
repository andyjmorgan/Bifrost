# The rules engine in the Bifrost gateway

The rules engine (`pkg/hooks`) sits behind Bifrost's front door. Every listing, call and reply goes through it. This page says how Bifrost calls the engine, the decisions that wiring took, and what was tested. It is a worked example for another gateway, which starts from [the handover page](handover.md).

## How a request flows

1. The Go SDK server accepts the request, as before. Its middleware (`internal/gateway/gateway.go`) hands every method to the engine layer.
2. `load` (`internal/gateway/hooks.go`) turns the caller's entitlements on the route into engine servers. Each gets its compiled rules, what the upstream said about itself, and the part of its catalog the request needs.
3. The entitlement's allow lists (its selection) cut the catalog first. The engine only ever sees what the selection left.
4. The engine answers the request, or says what to forward.
5. `relay` sends the forward upstream and takes the reply back through the engine, event by event. Progress is relayed as it arrives.

## What the gateway does and what the engine does

| The gateway | The engine |
| --- | --- |
| Transport: the SDK server at the front, HTTP to upstreams | What is listed, and under what name |
| Credentials and token refresh | What a call may carry, and whether it runs |
| Caching of catalogs and handshakes | What a reply may hold, including progress |
| The entitlement's allow lists | Confirmations and the upstream's own questions |
| Its own tools: `refresh_services`, and the search tools' definitions | Search over what the rules expose |

## Decisions made while wiring

- The front door stays the Go SDK server. This is how the Datadog gateway will sit, so it is the arrangement worth proving. Engine results go out through a result type that carries the engine's JSON unchanged.
- Every upstream call goes through the Go SDK client, whatever version the server speaks: there is no raw path. The gateway opens a session for each forward with the identity and capabilities the rules decided (`Connector.OpenFor`).
- The client library would answer a server's own question itself. That is switched off for forwards, so the question comes back as the call's result and goes on to the end client.
- The client library hands progress to its handler on a queue of its own, so a call can return before its last notifications are handled. The gateway closes the session, which waits for that queue, and only then puts the result in line. Progress therefore always arrives before the result, in order.
- A reply the client library cannot read ends the call with a JSON-RPC error, the lost-reply error `-32014`, and the cause goes to the record. The main case is a content block of a type the library does not know. Nothing of such a reply is passed on. Fields the library does not know are dropped silently, and notifications it does not know never surface.
- The client library reports a transport failure, such as a refused connection or an HTTP error, as a JSON-RPC error of its own (`-32005`). The gateway treats those as lost replies, not as the server's answer.
- A server on an older version gets no questions and no progress, by the earlier decision, so its reply is one result.
- `_meta` crosses the gateway only through two allowlists. The input list is for what comes into the gateway from a client: the request `_meta` keys that may go on to a server. A `server.session` rule still has to turn forwarding on; the list bounds what it can forward. The output list is for what comes from downstream: the `_meta` keys a server may send back, on catalog items and on the results of calls, prompts and reads. Both are empty unless set (`MCPGW_META_ALLOW_IN`, `MCPGW_META_ALLOW_OUT`), so by default no `_meta` passes either way. The protocol's own keys and the progress token are written by the gateway itself and cannot be put on either list.
- HTTP request headers reach rules only through an allow-list. `MCPGW_RULE_HEADERS` names the headers rules may read as `request.headers`, comma separated. It is empty unless set, so by default a rule sees no header. Headers that carry credentials are never shown.
- Every result names the gateway in `_meta`, under `io.modelcontextprotocol/serverInfo`, as the protocol asks of a server. The SDK server stamps it; the first wiring's result type dropped it. Discovery keeps the identity the rules chose.
- The state of a request that waits on an answer, to the gateway's question or to a server's, lives in the gateway's Postgres TTL cache, namespace `confirm`. Redeeming one deletes it in the same statement (`CacheTake`), so it works once across replicas. This replaces the engine's in-memory stand-in.
- Stored hooks that cannot be read, or whose rules do not compile, leave the entitlement exposing nothing. The API reports why in `rulesError`.

## How rules are stored

An entitlement keeps its rules as text, in one `jsonb` column on its row (`entitlements.hooks`). The object has one property per hook, holding the text of that hook's rules. A property that is absent means the hook has no rules:

```json
{
  "toolsListPost": "when tool.name == 'flip'\n  set tool.name = 'toss'\n",
  "toolsCallPre": "when tool.name == 'pick'\n  confirm \"Pick one?\"\n"
}
```

- The property is the hook's name with the separators dropped and the parts joined in camel case: `tools/call.post.result` is `toolsCallPostResult`. `ruletext.Property` gives it, and `ruletext.List` lists all 19 hooks.
- The text is the only stored form. The gateway compiles it to the engine's rules when it loads the entitlement, and keeps the result until the entitlement's generation changes.
- The API uses the same object, as the entitlement's `hooks`. `GET /entitlements/{id}` returns every property, with null where a hook has no rules. `PATCH` takes `hooks` and replaces all of them. A property that is not a hook's is refused.
- `POST /entitlements/validate` takes `hooks` and returns `problems`, `warnings` and `hasRules`. Each problem names its `hook` and `line`.
- `GET /entitlements/hooks-schema` describes every hook: what it controls, what a rule there may read and set, and a worked example.
- The engine's own input is still a list of JSON rules. Only the entitlement API and the database take text.
- A rule is switched off by commenting it out with `#`. A `//` comment may follow an expression on its line, and ends at the end of that line.
- `confirm "question"` compiles to the engine's `confirm` key alone. The engine decides whether the question has been answered.
- Rules are not counted. The API says only whether a hook, or an entitlement, has any.

## What was tested

| Test | What it proves |
| --- | --- |
| `internal/gateway/hooks_test.go`, 20 tests | The real front door over HTTP, with an in-memory store and mock upstreams: direct and mux routes, the selection cut, renames, result shaping, progress under the client's token, lost replies, confirmations through the store, the upstream's own question, older servers, unusable rules, prompts, resources, discovery, search mode, credentials, a reply the client library cannot read, and both `_meta` allowlists |
| `TestOfficialSDKClientsThroughTheGateway` | The official Go, Python and TypeScript client SDKs through the real front door: listing, a call with progress, a confirmation the SDK's own handler answers, a decline, a refusal, a prompt and a read |
| `internal/store/cache_test.go` | The take-once SQL against a real Postgres: 32 callers race for one confirmation and exactly one gets it. Needs `MCPGW_TEST_DSN` |
| A manual run | The gateway binary in dev mode, a real Postgres, and the Go SDK reference server: rules saved through the API, then listing, calls, progress, a confirmation, the server's own question and search mode |

All Go tests pass under the race detector. The race detector found one old bug, in `upstream.detach`, which is fixed.

## What this found

- The Go SDK server answers some JSON-RPC errors with an HTTP error status and a JSON body, not an event stream. An unknown tool is HTTP 400. The engine's design says the gateway always streams. Behind the SDK server that holds for results and for errors after the stream has started, and not for these. The official clients handle both.
- The Go SDK server requires the `Mcp-Name` header on calls, prompts and reads. The raw test client now sends it.
- A real SDK server stamps its name and version into `_meta` of every result. The engine passes a result's `_meta` on, so a client saw the upstream's identity there even when `server.settings` hides it in discovery. The output allowlist now removes it. The catalog allowlist the design already called for was also missing from the first wiring; the same list covers it.

## Known gaps

- `resources/directory/read` and the skills methods never reach the middleware: the SDK server answers "method not found" itself. Both belong to deferred extensions.
- The scenario cases (685) run against the test gateway in `pkg/hooks/harness`, not against this front door. The wiring tests cover the seams, not every rule.
- Faults and unknown shapes are logged and not stored. Rule hits are kept on the request's audit event, up to 100 per request.
- The engine's record of unknown shapes sees less than it would on a raw path: an unknown content block fails the call before the engine sees it, and unknown fields and notifications are dropped by the client library. Its rules for unknown things still apply to anything the library does pass.
- Tool names are matched exactly. Only the slug of a mux name is case-insensitive now.
- The upstream timeout bounds a whole streamed reply.
- A listing's `_meta` names an unreachable server with the transport's error text, which includes its address. That was already so, and suits an operator's own gateway; the Datadog gateway should not copy it.
- On a direct route with the `serverInfo` switch on, discovery names the upstream but every other result names the gateway.
- No real host application has been tried. On the live gateway, only a smoke test with a raw client has run: discovery, listings, calls on both routes, search mode, and one rename rule and one deny rule, since removed.

## Run it

```sh
go test ./internal/... -race -count=1
MATRIX_CLIENTS_REQUIRE=1 go test ./internal/gateway/ -run TestOfficialSDKClients -count=1 -v
MCPGW_TEST_DSN='postgres://…' go test ./internal/store/ -run TestCacheTake -count=1
```
