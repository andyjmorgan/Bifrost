package hooks

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// How rules run: a rule that fails, fails closed; an audit rule changes
// nothing; and the cost limit and the time budget refuse what goes over them.

// A value of the wrong type fails the rule when the request arrives, and the
// failure is closed: the switches go off, the tool is hidden, the call is refused.
func TestAWrongTypedSetFailsClosed(t *testing.T) {
	ctx := context.Background()
	s := newServer(t, oneTool(), Rule{Hook: "server.settings", When: "true", Then: map[string]string{"settings.prompts": "dyn('off')"}})
	if got := asJSON((&Engine{}).ListTools(ctx, newRequest("direct", s)).Result); !strings.Contains(got, `"tools":[]`) {
		t.Errorf("a settings rule that failed left tools listed: %s", got)
	}
	s = newServer(t, oneTool(), Rule{Hook: "tools/list.post", When: "true", Then: map[string]string{"tool.annotations.readOnlyHint": "dyn('yes')"}})
	if got := asJSON((&Engine{}).ListTools(ctx, newRequest("direct", s)).Result); !strings.Contains(got, `"tools":[]`) {
		t.Errorf("a list rule that failed left the tool listed: %s", got)
	}
	s = newServer(t, oneTool(), Rule{Hook: "tools/call.pre", When: "true", Then: map[string]string{"timeout": "dyn('30s')"}})
	if o := (&Engine{}).CallTool(ctx, newRequest("direct", s), &mcp.CallToolParamsRaw{Name: "t", Arguments: json.RawMessage(`{}`)}); o.Forward != nil {
		t.Error("a call rule that failed let the call through")
	}
	s = newServer(t, oneTool(), Rule{Hook: "tools/call.post.result", When: "true", Then: map[string]string{"response.result.content": "dyn(null)"}})
	if got := outcomeText(toolReply(t, s, `{"content":[{"type":"text","text":"SECRET"}]}`)); strings.Contains(got, "SECRET") {
		t.Errorf("a result rule that failed let the result through: %s", got)
	}
}

// Each expression draws its cost limit from Compile, as the gateway sets it.
// A rule that goes over the limit fails, and a failed enforce rule refuses
// the call.
func TestARuleOverItsCostLimitDeniesTheCall(t *testing.T) {
	rules := []Rule{{Hook: "tools/call.pre", When: "request.params.arguments.x.all(a, request.params.arguments.x.all(b, a == b))", Then: map[string]string{"request.params.arguments.q": "'changed'"}}}
	set, problems := Compile(rules, Options{CostLimit: 10})
	if problems != nil {
		t.Fatal(problems)
	}
	s := newServer(t, anyArgsTool)
	s.Rules = set
	o := (&Engine{}).CallTool(context.Background(), newRequest("direct", s), &mcp.CallToolParamsRaw{Name: "t", Arguments: json.RawMessage(`{"x":[1,1,1,1,1,1,1,1,1,1]}`)})
	if o.Forward != nil || !strings.Contains(hitErrors(o.Hits), "cost limit") {
		t.Errorf("cost limit did not deny: %s %s", outcomeText(o), hitErrors(o.Hits))
	}
}

// Rules that finish, but use up the request's time, must not let the
// request through: the budget is checked again after they ran. The rule
// here evaluates to false and so changes nothing; with a budget of one
// nanosecond, evaluating it spends the budget.
func TestRulesThatSucceedButSpendTheBudgetRefuseTheRequest(t *testing.T) {
	spent := func(t *testing.T, o Outcome) {
		t.Helper()
		if o.Forward != nil {
			t.Fatalf("forwarded after the rules spent the budget: %+v", o.Forward)
		}
		if o.Error == nil || !strings.Contains(o.Error.Message, "time budget") {
			t.Fatalf("want the budget refusal, got error %+v result %s", o.Error, o.Result)
		}
	}
	e := &Engine{Limits: Limits{Budget: time.Nanosecond}}
	server := func(rules ...Rule) Request {
		s := &Server{Slug: "a", Rules: mustCompileUncapped(t, rules...), Catalog: Catalog{
			Tools:     []*mcp.Tool{{Name: "t", InputSchema: map[string]any{"type": "object"}}},
			Prompts:   []*mcp.Prompt{{Name: "p"}},
			Resources: []*mcp.Resource{{URI: "docs://a", Name: "a"}},
		}}
		return Request{Route: "direct", Servers: []*Server{s}}
	}
	ctx := context.Background()
	noop := map[string]string{"decision": "'deny'"}

	t.Run("control: with time to spare the same requests forward", func(t *testing.T) {
		roomy := &Engine{Limits: Limits{Budget: time.Minute}}
		r := server(Rule{Hook: "tools/call.pre", When: "false", Then: noop}, Rule{Hook: "prompts/get.pre", When: "false", Then: noop}, Rule{Hook: "resources/read.pre", When: "false", Then: noop})
		if o := roomy.CallTool(ctx, r, &mcp.CallToolParamsRaw{Name: "t", Arguments: json.RawMessage(`{}`)}); o.Forward == nil {
			t.Fatalf("tool call did not forward: %+v", o)
		}
		if o := roomy.GetPrompt(ctx, r, &mcp.GetPromptParams{Name: "p"}); o.Forward == nil {
			t.Fatalf("prompt did not forward: %+v", o)
		}
		if o := roomy.ReadResource(ctx, r, &mcp.ReadResourceParams{URI: "docs://a"}); o.Forward == nil {
			t.Fatalf("read did not forward: %+v", o)
		}
	})
	t.Run("tools/call rules", func(t *testing.T) {
		r := server(Rule{Hook: "tools/call.pre", When: "false", Then: noop})
		spent(t, e.CallTool(ctx, r, &mcp.CallToolParamsRaw{Name: "t", Arguments: json.RawMessage(`{}`)}))
	})
	t.Run("server.session rules on a tool call", func(t *testing.T) {
		r := server(Rule{Hook: "server.session", When: "false", Then: map[string]string{"session._meta": "true"}})
		spent(t, e.CallTool(ctx, r, &mcp.CallToolParamsRaw{Name: "t", Arguments: json.RawMessage(`{}`)}))
	})
	t.Run("prompts/get rules", func(t *testing.T) {
		r := server(Rule{Hook: "prompts/get.pre", When: "false", Then: noop})
		spent(t, e.GetPrompt(ctx, r, &mcp.GetPromptParams{Name: "p"}))
	})
	t.Run("resources/read rules", func(t *testing.T) {
		r := server(Rule{Hook: "resources/read.pre", When: "false", Then: noop})
		spent(t, e.ReadResource(ctx, r, &mcp.ReadResourceParams{URI: "docs://a"}))
	})
	t.Run("server.session rules on a read", func(t *testing.T) {
		r := server(Rule{Hook: "server.session", When: "false", Then: map[string]string{"session._meta": "true"}})
		spent(t, e.ReadResource(ctx, r, &mcp.ReadResourceParams{URI: "docs://a"}))
	})
}

// A prompts/get whose only rule is a server.session rule is not forwarded
// once that rule has spent the request's time budget.
func TestAPromptIsNotForwardedOnceItsSessionRuleSpendsTheBudget(t *testing.T) {
	s := newServer(t, Catalog{Prompts: []*mcp.Prompt{{Name: "p"}}}, Rule{Hook: "server.session", When: "true", Then: map[string]string{"session._meta": "true"}})
	e := &Engine{Limits: Limits{Budget: time.Nanosecond}}
	o := e.GetPrompt(context.Background(), newRequest("direct", s), &mcp.GetPromptParams{Name: "p"})
	if o.Forward != nil {
		t.Errorf("prompt forwarded after its only rule exhausted the budget: %s", asJSON(o))
	}
}

// A request whose deadline has passed is refused on prompts/get,
// resources/read, prompts/list and resources/list, as it is on tools.
func TestAnExpiredRequestIsRefusedOnPromptsAndResources(t *testing.T) {
	e, r := serverRequest(t,
		Rule{Hook: "prompts/get.pre", When: "true", Then: map[string]string{"request.params.arguments.q": "'x'"}},
		Rule{Hook: "resources/read.pre", When: "false", Then: map[string]string{"decision": "'deny'"}},
		Rule{Hook: "prompts/list.post", When: "true", Then: map[string]string{"prompt.description": "'x'"}},
		Rule{Hook: "resources/list.post", When: "true", Then: map[string]string{"resource.description": "'x'"}},
	)
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	for name, run := range map[string]func() Outcome{
		"prompts/get":    func() Outcome { return e.GetPrompt(ctx, r, &mcp.GetPromptParams{Name: "p"}) },
		"resources/read": func() Outcome { return e.ReadResource(ctx, r, &mcp.ReadResourceParams{URI: "file:///r"}) },
		"prompts/list":   func() Outcome { return e.ListPrompts(ctx, r) },
		"resources/list": func() Outcome { return e.ListResources(ctx, r) },
	} {
		t.Run(name, func(t *testing.T) {
			out := run()
			if out.Error == nil {
				t.Fatalf("expired request returned success or forwarded: %+v", out)
			}
		})
	}
}

// A request that was cancelled, or whose enforce allowance is spent, does
// not get a tool listing.
func TestACancelledOrSpentRequestListsNoTools(t *testing.T) {
	ctx := context.Background()
	s := newServer(t, Catalog{Tools: anyArgsTool.Tools, Prompts: []*mcp.Prompt{{Name: "p"}}}, Rule{Hook: "server.settings", When: "true", Then: map[string]string{"settings.tasks": "true"}})
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if o := (&Engine{}).ListTools(cancelled, newRequest("direct", s)); o.Error == nil {
		t.Error("cancelled listing succeeded")
	}
	s = newServer(t, oneTool(), Rule{Hook: "tools/list.post", When: "true", Then: map[string]string{"tool.description": "'checked'"}})
	if o := (&Engine{Limits: Limits{Budget: time.Nanosecond}}).ListTools(ctx, newRequest("direct", s)); o.Error == nil {
		t.Error("exhausted listing succeeded")
	}
}

// An audit rule changes nothing, and that includes time: however long it
// runs, a call that is forwarded without it is forwarded with it.
func TestASlowAuditRuleNeverChangesACallsOutcome(t *testing.T) {
	e, r := serverRequest(t)
	e.Limits.Budget = 5 * time.Millisecond
	p := &mcp.CallToolParamsRaw{Name: "t"}
	baseline := e.CallTool(context.Background(), r, p)
	if baseline.Forward == nil {
		t.Fatalf("baseline did not forward: %+v", baseline)
	}
	r.Servers[0].Rules = mustCompileUncapped(t, Rule{Mode: "audit", Hook: "tools/call.pre", When: "lists.range(1000000).exists(i, i < 0)", Then: map[string]string{"decision": "'deny'"}})
	out := e.CallTool(context.Background(), r, p)
	if out.Forward == nil {
		b, _ := json.Marshal(out)
		t.Fatalf("audit-only work changed successful call into refusal: %s", b)
	}
}

// Time an audit rule spends is not charged to the enforce allowance: a slow
// audit rule between two enforce rules does not make the later one run out of
// time.
func TestASlowAuditRuleCannotExpireALaterEnforceRule(t *testing.T) {
	before := Rule{Hook: "tools/list.post", When: "false", Then: map[string]string{"decision": "'deny'"}}
	audit := Rule{Mode: "audit", Hook: "tools/list.post", When: "tool.description == 'slow'", Then: map[string]string{"decision": "'deny'"}}
	after := Rule{Hook: "tools/list.post", When: "lists.range(10).exists(i, i < 0)", Then: map[string]string{"decision": "'deny'"}}
	run := func(rs []Rule) outcome {
		set := mustCompileUncapped(t, rs...)
		// The audit rule takes longer than the whole enforce allowance. The
		// enforce rules themselves are cheap, so the allowance has room to
		// spare on a loaded machine: only audit time charged to enforcement
		// could use it up.
		ctx := withBudget(context.Background(), 300*time.Millisecond, 2*time.Second)
		resolve := func(name string) (any, bool) {
			if name == "description" {
				time.Sleep(600 * time.Millisecond)
				return "slow", true
			}
			return nil, false
		}
		return set.run(ctx, "tools/list.post", "t", resolve, Context{}.roots("tools/list", nil), nil)
	}
	if o := run([]Rule{before, after}); o.denied || o.err != nil {
		t.Fatalf("control failed: %+v", o)
	}
	if o := run([]Rule{before, audit, after}); o.denied || o.err != nil {
		t.Fatalf("audit-only work expired a later enforcement evaluation: %+v", o)
	}
}
