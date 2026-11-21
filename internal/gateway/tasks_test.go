package gateway_test

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/andyjmorgan/Bifrost/pkg/hooks"
	"github.com/andyjmorgan/Bifrost/pkg/hooks/harness"
)

// Tasks over the real front door: a client that asks for tasks on a call
// to a server that offers them gets a task, follows it with tasks/get,
// answers its question with tasks/update and gets the result, all through
// the rules.

var takesTasks = map[string]any{"elicitation": map[string]any{}, "extensions": map[string]any{hooks.ExtTasks: map[string]any{}}}

// optedIn reports whether a recorded request told the server the client
// takes tasks.
func optedIn(rec harness.Recorded) bool {
	meta, _ := rec.Map()["_meta"].(map[string]any)
	caps, _ := meta["io.modelcontextprotocol/clientCapabilities"].(map[string]any)
	exts, _ := caps["extensions"].(map[string]any)
	_, ok := exts[hooks.ExtTasks]
	return ok
}

func taskUpstream(t *testing.T) *harness.Upstream {
	u := harness.NewUpstream(t, harness.V2026)
	u.Extensions = map[string]any{hooks.ExtTasks: map[string]any{}}
	u.Tool(`{"name":"report"}`, func(rec harness.Recorded) harness.Reply {
		if !optedIn(rec) {
			return harness.Reply{Result: harness.Text("ran in place")}
		}
		return harness.Reply{RawResult: `{"resultType":"task","taskId":"up-1","status":"working","statusMessage":"Queued","ttlMs":60000,"pollIntervalMs":500}`}
	})
	var mu sync.Mutex
	polls, answered := 0, false
	u.Method("tasks/get", func(rec harness.Recorded) harness.Reply {
		mu.Lock()
		defer mu.Unlock()
		if rec.Map()["taskId"] != "up-1" {
			return harness.Reply{Error: &harness.RPCErr{Code: -32602, Message: "unknown task"}}
		}
		polls++
		switch {
		case polls == 1:
			return harness.Reply{RawResult: `{"resultType":"complete","taskId":"up-1","status":"working","statusMessage":"Half way"}`}
		case !answered:
			return harness.Reply{RawResult: `{"resultType":"complete","taskId":"up-1","status":"input_required","inputRequests":{"k1":{"method":"elicitation/create","params":{"mode":"form","message":"Approve?","requestedSchema":{"type":"object","properties":{"ok":{"type":"boolean"}}}}}}}`}
		}
		return harness.Reply{RawResult: `{"resultType":"complete","taskId":"up-1","status":"completed","result":{"content":[{"type":"text","text":"raw report"}]}}`}
	})
	u.Method("tasks/update", func(rec harness.Recorded) harness.Reply {
		mu.Lock()
		defer mu.Unlock()
		answered = true
		return harness.Reply{RawResult: `{"resultType":"complete"}`}
	})
	u.Method("tasks/cancel", func(harness.Recorded) harness.Reply {
		return harness.Reply{RawResult: `{"resultType":"complete"}`}
	})
	return u
}

func taskReq(method, id string, extra map[string]any) harness.RawRequest {
	p := map[string]any{"taskId": id}
	for k, v := range extra {
		p[k] = v
	}
	return harness.RawRequest{Method: method, Params: p, Capabilities: takesTasks}
}

func TestATaskRunsThroughTheDirectRoute(t *testing.T) {
	r := newRig(t)
	u := taskUpstream(t)
	r.serve("a", u,
		hooks.Rule{Hook: "tools/call.post.task", When: "has(task.statusMessage)", Then: map[string]string{"task.statusMessage": "'[' + tool.name + '] ' + task.statusMessage"}},
		hooks.Rule{Hook: "tools/call.post.result", When: "true", Then: map[string]string{"response.result.content": "[{'type': 'text', 'text': 'shaped report'}]"}},
	)

	disc := harness.Raw(t, r.direct("a"), harness.RawRequest{Method: "server/discover", Capabilities: takesTasks}).Result()
	if !strings.Contains(dump(disc), hooks.ExtTasks) {
		t.Fatalf("the direct route does not advertise tasks: %s", dump(disc))
	}
	if mux := harness.Raw(t, r.mux(), harness.RawRequest{Method: "server/discover", Capabilities: takesTasks}).Result(); strings.Contains(dump(mux), hooks.ExtTasks) {
		t.Fatalf("the mux advertises tasks: %s", dump(mux))
	}

	ask := call("report", nil)
	ask.Capabilities = takesTasks
	started := harness.Raw(t, r.direct("a"), ask).Result()
	id, _ := started["taskId"].(string)
	if started["resultType"] != "task" || !strings.HasPrefix(id, "t_") || strings.Contains(dump(started), "up-1") {
		t.Fatalf("the client got %s", dump(started))
	}
	if started["statusMessage"] != "[report] Queued" {
		t.Errorf("statusMessage = %v", started["statusMessage"])
	}
	if !optedIn(u.Last(t, "tools/call")) {
		t.Error("the server was not told the client takes tasks")
	}

	working := harness.Raw(t, r.direct("a"), taskReq("tasks/get", id, nil)).Result()
	if working["status"] != "working" || working["statusMessage"] != "[report] Half way" || working["taskId"] != id {
		t.Fatalf("poll 1 = %s", dump(working))
	}
	if h := u.Last(t, "tasks/get").Header.Get("Mcp-Name"); h != "up-1" {
		t.Errorf("the server's poll had Mcp-Name %q", h)
	}

	asking := harness.Raw(t, r.direct("a"), taskReq("tasks/get", id, nil)).Result()
	if asking["status"] != "input_required" || !strings.Contains(dump(asking), "Approve?") {
		t.Fatalf("poll 2 = %s", dump(asking))
	}
	upd := harness.Raw(t, r.direct("a"), taskReq("tasks/update", id, map[string]any{"inputResponses": map[string]any{"k1": map[string]any{"action": "accept", "content": map[string]any{"ok": true}}}}))
	if upd.Result() == nil {
		t.Fatalf("update = %v", upd.Final())
	}
	if got := dump(u.Last(t, "tasks/update").Map()["inputResponses"]); !strings.Contains(got, `"ok":true`) {
		t.Errorf("the server got answers %s", got)
	}

	done := harness.Raw(t, r.direct("a"), taskReq("tasks/get", id, nil)).Result()
	if done["status"] != "completed" || !strings.Contains(dump(done), "shaped report") || strings.Contains(dump(done), "raw report") {
		t.Fatalf("poll 3 = %s", dump(done))
	}

	// A caller who did not start the task, or the mux route, cannot follow it.
	if rep := harness.Raw(t, r.mux(), taskReq("tasks/get", id, nil)); rep.Result() != nil {
		t.Errorf("the mux answered a poll: %v", rep.Final())
	}
}

func TestACallWhoseClientDoesNotAskForTasksRunsInPlace(t *testing.T) {
	r := newRig(t)
	u := taskUpstream(t)
	r.serve("a", u)
	if got := text(harness.Raw(t, r.direct("a"), call("report", nil))); got != "ran in place" {
		t.Fatalf("the call returned %q", got)
	}
	if optedIn(u.Last(t, "tools/call")) {
		t.Error("the server was told the client takes tasks")
	}
	ask := call("report", nil)
	ask.Capabilities = takesTasks
	if got := text(harness.Raw(t, r.mux(), harness.RawRequest{Method: "tools/call", Params: map[string]any{"name": "a__report", "arguments": map[string]any{}}, Capabilities: takesTasks})); got != "ran in place" {
		t.Fatalf("the mux call returned %q", got)
	}
}

func TestATaskDeniedByARuleIsCancelledAtTheServer(t *testing.T) {
	r := newRig(t)
	u := taskUpstream(t)
	r.serve("a", u, deny("tools/call.post.task", "true"))
	ask := call("report", nil)
	ask.Capabilities = takesTasks
	rep := harness.Raw(t, r.direct("a"), ask).Result()
	if rep["isError"] != true || strings.Contains(dump(rep), "up-1") {
		t.Fatalf("the client got %s", dump(rep))
	}
	waitFor(t, func() bool { return len(u.Requests("tasks/cancel")) == 1 })
}

// waitFor waits up to five seconds for cond.
func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("timed out")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
