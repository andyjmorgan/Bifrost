# tools/call.post.task

`tools/call.post.task` rewords or ends a tool call that runs as a task, each time the client is told the task's status.

## When it runs

A server that supports the MCP tasks extension can answer a tool call with a task: it accepts the job, answers at once with a task id, and does the work in the background. The client then asks for the task's status with `tasks/get` until it finishes, answers its questions with `tasks/update`, and can stop it with `tasks/cancel`.

The gateway passes tasks through only when all of these are true:

- the request comes in on a direct route, `/<slug>/mcp`
- the server advertises the `io.modelcontextprotocol/tasks` extension and speaks MCP 2026-07-28
- the client declares the extension on the call itself, in the call's client capabilities
- `settings.tasks` in [server.settings](server-settings.md) is on, which is the default

Otherwise the server is not told the client can take a task, and the call runs as an ordinary call.

`tools/call.post.task` runs when the server answers the call with a task, and on each `tasks/get` while the task is `working` or `input_required`. It does not run when the task has finished. A finished task's result goes through [tools/call.post.result](tools-call-post-result.md), and a task's questions go through [tools/call.post.elicit](tools-call-post-elicit.md). Both see the tree of the call that started the task.

## What a rule can read

| Branch | Notable fields |
| --- | --- |
| `task` | The status as the client will get it: `task.status`, which is `working` or `input_required`. `task.statusMessage`, `task.createdAt`, `task.lastUpdatedAt`, `task.ttlMs` and `task.pollIntervalMs`, when the server sent them |
| `task.upstream` | The status as the server sent it. It never changes |
| `request.params` | The call that started the task: `request.params.name` and `request.params.arguments`, under the names the client knows |
| `request.upstream` | The call as the server received it: `request.upstream.params.name` and `request.upstream.params.arguments` |
| `request` | Also `request.method`, `request.caller`, `request.client`, `request.headers`, `request.route`, `request.flags`, `request.time` |
| `tool` | The tool as the client sees it, and `tool.upstream` as the server declares it |
| `server` | `server.id`, `server.slug` and what the gateway knows about the server |

The task's id is not in the tree. The client never sees the server's id: the gateway gives the client an id of its own.

`task.statusMessage` is absent when the server sent none. Read it with the [optional forms](overview.md#optional-values): `task.?statusMessage.orValue('')`, or guard with `has(task.statusMessage)`.

## What a rule can set

| Path | Type | Effect | Stops evaluation |
| --- | --- | --- | --- |
| `task.statusMessage` | string | Replace the status message the client sees. Later rules read the new value | No |

A rule here can deny. It cannot ask the user to confirm.

| Key | Value | Effect | Stops evaluation |
| --- | --- | --- | --- |
| `decision` | `'deny'` | End the task. The gateway cancels it at the server | Yes |
| `reason` | string | What the client is told about why the task ended | No |

A rule cannot change the status, the times or the poll interval.

## What the client sees

| Outcome | When the task starts | On a later `tasks/get` |
| --- | --- | --- |
| No rule matches | The task, with the gateway's id and the fields the server sent | The status, with the fields the server sent |
| A rule sets `task.statusMessage` | The task with the new message | The status with the new message |
| A rule denies | An error result with the reason. The task is cancelled at the server | The status `failed`, with the reason as the error message. The task is cancelled at the server |
| A rule fails to evaluate | As a deny, with the hook's generic refusal | As a deny, with the hook's generic refusal |

## Examples

### Reword the server's status messages

```json
{
  "name": "Name the tool in task status",
  "hook": "tools/call.post.task",
  "when": "has(task.statusMessage)",
  "then": { "task.statusMessage": "tool.name + ': ' + task.statusMessage" }
}
```

A task of the tool `export` whose server reports `Compiling, 40 of 212 files done` reaches the client as `export: Compiling, 40 of 212 files done`. A status with no message is unchanged.

### End background work for API keys

```json
{
  "name": "No background work for keys",
  "hook": "tools/call.post.task",
  "when": "request.caller.type == 'apikey'",
  "then": { "decision": "'deny'", "reason": "'Background work is not available to API keys.'" }
}
```

A call made with an API key that the server starts as a task ends at once. The client gets an error result that says `Background work is not available to API keys.`, and the gateway cancels the task at the server. To stop such calls running as tasks at all, switch `settings.tasks` off for those callers instead. The server then runs the call as an ordinary call, or refuses it if it only runs as a task.

### Say plainly that the task is waiting

```json
{
  "name": "Waiting for an answer",
  "hook": "tools/call.post.task",
  "when": "task.status == 'input_required'",
  "then": { "task.statusMessage": "'Waiting for your answer.'" }
}
```

While the task waits for the user, the client's status message is `Waiting for your answer.`, whatever the server said.

## Nuances and limits

- Tasks pass through on a direct route only. On the mux route the server is never told the client can take a task.
- Only the person who started a task can follow it, on the same route. A poll from anyone else, or for an id the gateway did not issue, is told the task is unknown.
- When the server's rules, selection or catalog change, a task started before the change ends. Its next poll reports it `failed`, and the gateway cancels it at the server.
- A task that asks for input is ended when the client cannot answer questions, or when a [tools/call.post.elicit](tools-call-post-elicit.md) rule denies the question.
- `tasks/update` passes on answers only to the questions the client was last asked.
- The gateway keeps a task's record for the `ttlMs` the server gives, at most 24 hours, and one hour when the server gives none.
- A failed or cancelled task's error passes through as the server sent it. No rule sees it.
- Audit-mode rules never change a status. They record what they would have done.

## Related pages

- [Hooks and rules overview](overview.md)
- [server.settings for the tasks switch](server-settings.md)
- [tools/call.post.result for a finished task's result](tools-call-post-result.md)
- [tools/call.post.elicit for a task's questions](tools-call-post-elicit.md)
