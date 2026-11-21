package hooks

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Tasks pass through on the direct route, for a server that offers them and
// a client that asks for them on the call. Everything that reaches the
// client goes through the rules.

// taskServer is a current server that advertises the tasks extension, with
// the tool t.
func taskServer(t *testing.T, rules ...Rule) *Server {
	t.Helper()
	s := newServer(t, anyArgsTool, rules...)
	s.ID = "srv-1"
	s.Info.Extensions = map[string]any{ExtTasks: map[string]any{}}
	return s
}

// asksForTasks is a request whose client declares the tasks extension.
func asksForTasks(route string, s *Server) Request {
	r := newRequest(route, s)
	r.Client = map[string]any{"capabilities": map[string]any{"elicitation": map[string]any{}, "extensions": map[string]any{ExtTasks: map[string]any{}}}}
	return r
}

const taskHandle = `{"resultType":"task","taskId":"up-77","status":"working","statusMessage":"Queued","createdAt":"2026-11-21T00:00:00Z","lastUpdatedAt":"2026-11-21T00:00:00Z","ttlMs":60000,"pollIntervalMs":1000}`

// startTask makes the call t and has the server answer with taskHandle. It
// returns the client's task id.
func startTask(t *testing.T, e *Engine, r Request) (string, Outcome) {
	t.Helper()
	p := &mcp.CallToolParamsRaw{Name: "t", Arguments: json.RawMessage(`{"q":"x"}`)}
	call := e.CallTool(context.Background(), r, p)
	if call.Forward == nil {
		t.Fatalf("not forwarded: %s", outcomeText(call))
	}
	if _, ok := call.Forward.Extensions[ExtTasks]; !ok {
		t.Fatalf("the server was not offered tasks: %+v", call.Forward)
	}
	o := e.UpstreamToolReply(context.Background(), r, call.Call, p, json.RawMessage(taskHandle))
	m, _ := o.Result.(map[string]any)
	id, _ := m["taskId"].(string)
	return id, o
}

func TestTheServerIsOfferedTasksOnlyWhenEverythingAllowsIt(t *testing.T) {
	ctx := context.Background()
	offered := func(s *Server, r Request) bool {
		o := (&Engine{}).CallTool(ctx, r, &mcp.CallToolParamsRaw{Name: lowerName(r, "t")})
		if o.Forward == nil {
			t.Fatalf("not forwarded: %s", outcomeText(o))
		}
		_, ok := o.Forward.Extensions[ExtTasks]
		return ok
	}
	s := taskServer(t)
	if !offered(s, asksForTasks("direct", s)) {
		t.Error("a direct call whose client asked for tasks was not offered them")
	}
	if offered(s, newRequest("direct", s)) {
		t.Error("a call whose client did not ask for tasks was offered them")
	}
	if offered(s, asksForTasks("mux", s)) {
		t.Error("a mux call was offered tasks")
	}
	plain := taskServer(t)
	plain.Info.Extensions = nil
	if offered(plain, asksForTasks("direct", plain)) {
		t.Error("a server that does not advertise tasks was offered them")
	}
	old := taskServer(t)
	old.Info.ProtocolVersion = "2025-11-25"
	if offered(old, asksForTasks("direct", old)) {
		t.Error("a server on an older version was offered tasks")
	}
	off := taskServer(t, Rule{Hook: "server.settings", When: "true", Then: map[string]string{"settings.tasks": "false"}})
	if offered(off, asksForTasks("direct", off)) {
		t.Error("a server whose tasks switch is off was offered tasks")
	}
}

func lowerName(r Request, name string) string {
	if r.mux() {
		return r.Servers[0].Slug + Separator + name
	}
	return name
}

func TestDiscoveryAdvertisesTheServersExtensionsOnTheDirectRouteOnly(t *testing.T) {
	ctx := context.Background()
	s := taskServer(t)
	s.Info.Extensions = map[string]any{ExtTasks: map[string]any{"x": 1}, "com.example/other": map[string]any{}}
	direct := (&Engine{}).Discover(ctx, newRequest("direct", s), "test")
	caps := direct.Result.(DiscoverResult).Capabilities
	if got := asJSON(caps["extensions"]); got != `{"io.modelcontextprotocol/tasks":{"x":1}}` {
		t.Errorf("direct extensions = %s, want the server's tasks settings and nothing else", got)
	}
	mux := (&Engine{}).Discover(ctx, newRequest("mux", s), "test")
	if _, ok := mux.Result.(DiscoverResult).Capabilities["extensions"]; ok {
		t.Errorf("the mux advertised extensions: %v", mux.Result.(DiscoverResult).Capabilities)
	}
}

func TestATaskHandleReachesTheClientUnderTheGatewaysOwnID(t *testing.T) {
	s := taskServer(t, Rule{Hook: "tools/call.post.task", When: "task.status == 'working'", Then: map[string]string{"task.statusMessage": "'Running: ' + task.upstream.?statusMessage.orValue('')"}})
	e := &Engine{}
	id, o := startTask(t, e, asksForTasks("direct", s))
	got := outcomeText(o)
	if !strings.HasPrefix(id, taskIDPrefix) || strings.Contains(got, "up-77") {
		t.Fatalf("the client got %s", got)
	}
	m := o.Result.(map[string]any)
	if m["resultType"] != "task" || m["status"] != "working" || m["statusMessage"] != "Running: Queued" || m["pollIntervalMs"] == nil {
		t.Errorf("handle = %s", got)
	}
	if e.tasks().(*MemoryTasks).Len() != 1 {
		t.Error("the task was not recorded")
	}
}

func TestATaskRuleThatDeniesEndsTheTaskAtTheServer(t *testing.T) {
	s := taskServer(t, Rule{Hook: "tools/call.post.task", When: "true", Then: map[string]string{"decision": "'deny'", "reason": "'No background work.'"}})
	e := &Engine{}
	_, o := startTask(t, e, asksForTasks("direct", s))
	if !strings.Contains(outcomeText(o), "No background work.") || !strings.Contains(outcomeText(o), `"isError":true`) {
		t.Errorf("the client got %s", outcomeText(o))
	}
	if o.Cancel == nil || o.Cancel.Method != "tasks/cancel" || asJSON(o.Cancel.Params) != `{"taskId":"up-77"}` {
		t.Errorf("the task was not cancelled at the server: %+v", o.Cancel)
	}
	if e.tasks().(*MemoryTasks).Len() != 0 {
		t.Error("a denied task was recorded")
	}
}

func TestOnlyWhoStartedATaskCanFollowIt(t *testing.T) {
	ctx := context.Background()
	s := taskServer(t)
	e := &Engine{}
	r := asksForTasks("direct", s)
	id, _ := startTask(t, e, r)

	other := r
	other.Principal = "someone-else"
	for name, o := range map[string]Outcome{
		"another principal": e.GetTask(ctx, other, id),
		"the mux route":     e.GetTask(ctx, asksForTasks("mux", s), id),
		"an unknown id":     e.GetTask(ctx, r, taskIDPrefix+"nope"),
		"the server's id":   e.GetTask(ctx, r, "up-77"),
	} {
		if o.Error == nil || o.Error.Code != CodeInvalidParams || o.Forward != nil {
			t.Errorf("%s: %s", name, outcomeText(o))
		}
	}
	o := e.GetTask(ctx, r, id)
	if o.Forward == nil || o.Forward.Method != "tasks/get" || asJSON(o.Forward.Params) != `{"taskId":"up-77"}` {
		t.Fatalf("the owner's poll was not forwarded: %s %+v", outcomeText(o), o.Forward)
	}
}

func TestAPollGoesThroughTheRules(t *testing.T) {
	ctx := context.Background()
	s := taskServer(t,
		Rule{Hook: "tools/call.post.task", When: "has(task.statusMessage)", Then: map[string]string{"task.statusMessage": "'[' + tool.name + '] ' + task.statusMessage"}},
		Rule{Hook: "tools/call.post.elicit", When: "true", Then: map[string]string{"elicitation.params.message": "'Asked: ' + elicitation.params.message"}},
		Rule{Hook: "tools/call.post.result", When: "true", Then: map[string]string{"response.result.content": "[{'type': 'text', 'text': 'shaped ' + request.params.arguments.q}]"}},
	)
	e := &Engine{}
	r := asksForTasks("direct", s)
	id, _ := startTask(t, e, r)
	poll := func(reply string) Outcome {
		t.Helper()
		o := e.GetTask(ctx, r, id)
		if o.Task == nil {
			t.Fatalf("no forward: %s", outcomeText(o))
		}
		return e.UpstreamTaskReply(ctx, r, o.Task, json.RawMessage(reply))
	}

	working := poll(`{"taskId":"up-77","status":"working","statusMessage":"Half way"}`)
	if got := outcomeText(working); !strings.Contains(got, `"statusMessage":"[t] Half way"`) || !strings.Contains(got, id) || strings.Contains(got, "up-77") {
		t.Errorf("working = %s", got)
	}

	asking := poll(`{"taskId":"up-77","status":"input_required","inputRequests":{"k1":{"method":"elicitation/create","params":{"message":"Approve?","requestedSchema":{"type":"object","properties":{}}}}}}`)
	if got := outcomeText(asking); !strings.Contains(got, "Asked: Approve?") || !strings.Contains(got, `"k1"`) {
		t.Errorf("input_required = %s", got)
	}
	if o := e.UpdateTask(ctx, r, id, map[string]json.RawMessage{"other": json.RawMessage(`{"action":"accept"}`)}); o.Error == nil || o.Forward != nil {
		t.Errorf("an answer to a question the task did not ask went on: %s", outcomeText(o))
	}
	up := e.UpdateTask(ctx, r, id, map[string]json.RawMessage{"k1": json.RawMessage(`{"action":"accept","content":{"ok":true}}`)})
	if up.Forward == nil || asJSON(up.Forward.Params) != `{"inputResponses":{"k1":{"action":"accept","content":{"ok":true}}},"taskId":"up-77"}` {
		t.Fatalf("update forward = %s %+v", outcomeText(up), up.Forward)
	}
	if got := outcomeText(e.UpstreamTaskReply(ctx, r, up.Task, json.RawMessage(`{}`))); got != `{"result":{"resultType":"complete"},"error":null}` {
		t.Errorf("update reply = %s", got)
	}
	if o := e.UpdateTask(ctx, r, id, map[string]json.RawMessage{"k1": json.RawMessage(`{"action":"accept"}`)}); o.Error == nil {
		t.Errorf("a question was answered twice: %s", outcomeText(o))
	}

	done := poll(`{"taskId":"up-77","status":"completed","result":{"content":[{"type":"text","text":"raw"}]}}`)
	if got := outcomeText(done); !strings.Contains(got, "shaped x") || strings.Contains(got, `"raw"`) || !strings.Contains(got, `"status":"completed"`) {
		t.Errorf("completed = %s", got)
	}

	failed := poll(`{"taskId":"up-77","status":"failed","error":{"code":-32603,"message":"boom"}}`)
	if got := outcomeText(failed); !strings.Contains(got, `"message":"boom"`) || !strings.Contains(got, `"status":"failed"`) {
		t.Errorf("failed = %s", got)
	}
}

func TestAChangeOfPolicyEndsATask(t *testing.T) {
	ctx := context.Background()
	s := taskServer(t)
	e := &Engine{}
	r := asksForTasks("direct", s)
	id, _ := startTask(t, e, r)
	s.Generation = "2"
	o := e.GetTask(ctx, r, id)
	if o.Forward != nil || !strings.Contains(outcomeText(o), `"status":"failed"`) || !strings.Contains(outcomeText(o), "policy changed") {
		t.Fatalf("poll after a change = %s", outcomeText(o))
	}
	if o.Cancel == nil || asJSON(o.Cancel.Params) != `{"taskId":"up-77"}` {
		t.Errorf("the task was not cancelled at the server: %+v", o.Cancel)
	}
	if again := e.GetTask(ctx, r, id); again.Error == nil || again.Error.Code != CodeInvalidParams {
		t.Errorf("an ended task is still known: %s", outcomeText(again))
	}
}

func TestAQuestionFromATaskIsRefusedForAClientThatCannotAnswer(t *testing.T) {
	ctx := context.Background()
	s := taskServer(t)
	e := &Engine{}
	r := asksForTasks("direct", s)
	r.Client = map[string]any{"capabilities": map[string]any{"extensions": map[string]any{ExtTasks: map[string]any{}}}}
	id, _ := startTask(t, e, r)
	o := e.GetTask(ctx, r, id)
	got := e.UpstreamTaskReply(ctx, r, o.Task, json.RawMessage(`{"taskId":"up-77","status":"input_required","inputRequests":{"k1":{"method":"elicitation/create","params":{"message":"Approve?"}}}}`))
	if !strings.Contains(outcomeText(got), `"status":"failed"`) || strings.Contains(outcomeText(got), "Approve?") || got.Cancel == nil {
		t.Errorf("the client got %s, cancel %+v", outcomeText(got), got.Cancel)
	}
}

// The examples on the hook's page do what the page says.
func TestTheTaskHookPageExamples(t *testing.T) {
	b, err := os.ReadFile("../../docs/rules/hooks/tools-call-post-task.md")
	if err != nil {
		t.Skip("docs not present: " + err.Error())
	}
	var rules []Rule
	for _, block := range strings.Split(string(b), "```json\n")[1:] {
		var r Rule
		if err := json.Unmarshal([]byte(strings.SplitN(block, "```", 2)[0]), &r); err != nil {
			t.Fatalf("an example is not a rule: %v", err)
		}
		rules = append(rules, r)
	}
	if len(rules) != 3 {
		t.Fatalf("%d examples, want 3", len(rules))
	}
	ctx := context.Background()
	start := func(rule Rule, caller map[string]any, handle string) Outcome {
		s := newServer(t, Catalog{Tools: []*mcp.Tool{{Name: "export", InputSchema: map[string]any{"type": "object"}}}}, rule)
		s.ID, s.Info.Extensions = "srv-1", map[string]any{ExtTasks: map[string]any{}}
		r := asksForTasks("direct", s)
		if caller != nil {
			r.Caller = caller
		}
		e := &Engine{}
		p := &mcp.CallToolParamsRaw{Name: "export"}
		call := e.CallTool(ctx, r, p)
		return e.UpstreamToolReply(ctx, r, call.Call, p, json.RawMessage(handle))
	}
	o := start(rules[0], nil, `{"resultType":"task","taskId":"x","status":"working","statusMessage":"Compiling, 40 of 212 files done"}`)
	if got := outcomeText(o); !strings.Contains(got, `"statusMessage":"export: Compiling, 40 of 212 files done"`) {
		t.Errorf("example 1: %s", got)
	}
	o = start(rules[1], map[string]any{"userId": "u", "type": "apikey"}, `{"resultType":"task","taskId":"x","status":"working"}`)
	if got := outcomeText(o); !strings.Contains(got, "Background work is not available to API keys.") || o.Cancel == nil {
		t.Errorf("example 2: %s", got)
	}
	o = start(rules[2], nil, `{"resultType":"task","taskId":"x","status":"input_required","statusMessage":"Need approval"}`)
	if got := outcomeText(o); !strings.Contains(got, `"statusMessage":"Waiting for your answer."`) {
		t.Errorf("example 3: %s", got)
	}
}
