package hooks

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Tests for the checks the November 15 test gap audit found untested and
// that the JSON case format cannot reach.

// A rule with a misspelt mode must be refused when it is saved. If this
// check went, "audt" would not equal "audit" and the rule would enforce.
func TestGapAuditCompileRefusesBadShape(t *testing.T) {
	for _, tc := range []struct {
		name  string
		rule  Rule
		field string
	}{
		{"a misspelt mode", Rule{Hook: "tools/call.pre", Mode: "audt", When: "true", Then: map[string]string{"decision": "'deny'"}}, "mode"},
		{"an empty when", Rule{Hook: "tools/call.pre", When: "  ", Then: map[string]string{"decision": "'deny'"}}, "when"},
		{"a rule with no then", Rule{Hook: "tools/call.pre", When: "true"}, "then"},
		{"a filter rule with no then", Rule{Hook: "tools/list.post", When: "true"}, "then"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			set, probs := Compile([]Rule{tc.rule})
			if len(probs) == 0 {
				t.Fatalf("the rule was accepted: %+v", tc.rule)
			}
			found := false
			for _, p := range probs {
				found = found || p.Field == tc.field
			}
			if !found {
				t.Fatalf("no problem names the field %q: %v", tc.field, probs)
			}
			if set != nil && set.has(tc.rule.Hook) {
				t.Fatalf("a rule that was refused is still in the set")
			}
		})
	}
}

// Rules that finish, but use up the request's time, must not let the
// request through: the budget is checked again after they ran. The rule
// here evaluates to false and so changes nothing; with a budget of one
// nanosecond, evaluating it spends the budget.
func TestGapAuditBudgetSpentByRulesThatSucceed(t *testing.T) {
	spent := func(t *testing.T, o Outcome) {
		t.Helper()
		if o.Forward != nil {
			t.Fatalf("forwarded after the rules spent the budget: %+v", o.Forward)
		}
		if o.Error == nil || !strings.Contains(o.Error.Message, "time budget") {
			t.Fatalf("want the budget refusal, got error %+v result %s", o.Error, o.Result)
		}
	}
	e := &Engine{Signer: Signer{Key: []byte("k")}, Limits: Limits{Budget: time.Nanosecond}}
	server := func(rules ...Rule) Request {
		s := &Server{Slug: "a", Rules: codexSet(t, rules...), Catalog: Catalog{
			Tools:     []*mcp.Tool{{Name: "t", InputSchema: map[string]any{"type": "object"}}},
			Prompts:   []*mcp.Prompt{{Name: "p"}},
			Resources: []*mcp.Resource{{URI: "docs://a", Name: "a"}},
		}}
		return Request{Route: "direct", Servers: []*Server{s}}
	}
	ctx := context.Background()
	noop := map[string]string{"decision": "'deny'"}

	t.Run("control: with time to spare the same requests forward", func(t *testing.T) {
		roomy := &Engine{Signer: Signer{Key: []byte("k")}, Limits: Limits{Budget: time.Minute}}
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
		r := server(Rule{Hook: "server.session", When: "false", Then: map[string]string{"meta": "true"}})
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
		r := server(Rule{Hook: "server.session", When: "false", Then: map[string]string{"meta": "true"}})
		spent(t, e.ReadResource(ctx, r, &mcp.ReadResourceParams{URI: "docs://a"}))
	})
}

// The filters that decide which server-chosen names reach an unknown
// record. They guard what is written to logs.
func TestGapAuditUnknownRecordNameFilters(t *testing.T) {
	for k, want := range map[string]bool{
		"type": true, "_meta": true, "$schema": true, "mime-type": true, "v2": true, "a123": true,
		"": false, "1abc": false, "-x": false, "a1234": false, "alice@example.com": false,
		"path/to": false, "has space": false, "a$b": false, "naïve": false,
		"abcdefghijklmnopqrstuvwxyzabcdefg":    false, // 33 characters
		"550e8400-e29b-41d4-a716-446655440000": false,
	} {
		if got := fieldName(k); got != want {
			t.Errorf("fieldName(%q) = %v, want %v", k, got, want)
		}
	}
	for name, kept := range map[string]bool{
		"notifications/tools/list_changed": true, "resource_link": true, "x.y-z": true, "v123456": true,
		"alice@example.com": false, "with space": false, "id-1234567": false,
		strings.Repeat("a", 65): false,
	} {
		got := safeName(name)
		if kept && got != name {
			t.Errorf("safeName(%q) = %q, want it kept", name, got)
		}
		if !kept && (got == name || strings.Contains(got, name)) {
			t.Errorf("safeName(%q) = %q, want the name withheld", name, got)
		}
	}
	for in, want := range map[string]string{
		"docs://a/b?token=secret": "docs://a/b",
		"docs://a/b#frag":         "docs://a/b",
		"plain":                   "plain",
	} {
		if got := safeTarget(in); got != want {
			t.Errorf("safeTarget(%q) = %q, want %q", in, got, want)
		}
	}
	if got := safeTarget("x://" + strings.Repeat("a", 400)); len([]rune(got)) != 257 || !strings.HasSuffix(got, "…") {
		t.Errorf("a long target is not cut at 256 characters: %d", len([]rune(got)))
	}
	for in, lacks := range map[string]string{
		"{/customerPath*}": "customerPath",
		"{?secretQuery}":   "secretQuery",
		"{id}":             "id}",
	} {
		if got := templateForm(in); strings.Contains(got, lacks) {
			t.Errorf("templateForm(%q) = %q, which still holds the variable name", in, got)
		}
	}
}

// The case runner must not pass a check that had nothing to check.
func TestGapAuditRunnerChecksNeedSomethingToCheck(t *testing.T) {
	const errorOnly = `{"client":{"error":{"code":-32603,"message":"broken"}}}`
	const clientFailed = `{"client":{"error":{"code":0,"message":"the SDK call failed: timeout"}}}`
	const toolError = `{"client":{"result":{"isError":true,"content":[]}}}`
	const fine = `{"client":{"result":{"resultType":"complete","content":[{"type":"text","text":"ok"}]}}}`
	for _, tc := range []struct {
		name, expect, report string
		wantPass             bool
	}{
		{"notEquals on a missing value", `{"path":"client.result.resultType","notEquals":"task"}`, errorOnly, false},
		{"notEquals on a present value", `{"path":"client.result.resultType","notEquals":"task"}`, fine, true},
		{"notMatches on a missing value", `{"path":"client.result.instructions","notMatches":"."}`, errorOnly, false},
		{"lacksText on a missing value", `{"path":"client.result.content","lacksText":["secret"]}`, errorOnly, false},
		{"lacksText on a present value", `{"path":"client.result.content","lacksText":["secret"]}`, fine, true},
		{"absence is asserted with exists", `{"path":"client.result.task","exists":false}`, errorOnly, true},
		{"refused: a JSON-RPC error", `{"path":"client","refused":true}`, errorOnly, true},
		{"refused: a tool error", `{"path":"client","refused":true}`, toolError, true},
		{"refused: a client that only failed is not a refusal", `{"path":"client","refused":true}`, clientFailed, false},
		{"refused: a normal result", `{"path":"client","refused":true}`, fine, false},
		{"not refused: a normal result", `{"path":"client","refused":false}`, fine, true},
		{"not refused: a client that only failed has no result, so it did not succeed", `{"path":"client","refused":false}`, clientFailed, false},
		{"not refused: a JSON-RPC error", `{"path":"client","refused":false}`, errorOnly, false},
		{"not refused: a tool error", `{"path":"client","refused":false}`, toolError, false},
		{"[*] equals [] on a missing list", `{"path":"client.result.tools[*].name","equals":[]}`, errorOnly, false},
		{"[*] equals [] on a present, empty list", `{"path":"client.result.tools[*].name","equals":[]}`, `{"client":{"result":{"tools":[]}}}`, true},
		{"[*] equals [] on a list with elements", `{"path":"client.result.tools[*].name","equals":[]}`, `{"client":{"result":{"tools":[{"name":"t"}]}}}`, false},
		{"[*] len 0 on a missing list", `{"path":"client.result.tools[*].name","len":0}`, errorOnly, false},
		{"[*] len 0 on a present, empty list", `{"path":"client.result.tools[*].name","len":0}`, `{"client":{"result":{"tools":[]}}}`, true},
		{"[*] notContains on a missing list", `{"path":"client.result.tools[*].name","notContains":"secret"}`, errorOnly, false},
		{"[*] notContains on a present, empty list", `{"path":"client.result.tools[*].name","notContains":"secret"}`, `{"client":{"result":{"tools":[]}}}`, true},
		{"[*] set [] on a missing list", `{"path":"client.result.tools[*].name","set":[]}`, errorOnly, false},
		{"[*] set [] on a present, empty list", `{"path":"client.result.tools[*].name","set":[]}`, `{"client":{"result":{"tools":[]}}}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var ex Expect
			if err := json.Unmarshal([]byte(tc.expect), &ex); err != nil {
				t.Fatal(err)
			}
			var report any
			if err := json.Unmarshal([]byte(tc.report), &report); err != nil {
				t.Fatal(err)
			}
			if failure := ex.Check(report, nil); (failure == "") != tc.wantPass {
				t.Fatalf("pass=%v, want %v; diagnostic=%q", failure == "", tc.wantPass, failure)
			}
		})
	}
}

// A misspelt key in a case must stop the case loading. Ignored, it would
// leave a case that tests nothing and passes.
func TestGapAuditCaseLoaderRefusesUnknownKeys(t *testing.T) {
	ok := `[{"name":"n","servers":[{"slug":"a","catalog":{"tools":[{"name":"t","inputSchema":{"type":"object","x-anything":1}}]}}],
	 "steps":[{"op":"tools/call","params":{"name":"t","arguments":{"any":{"thing":1}}},"upstream":{"content":[],"whatever":true},"expect":[{"path":"client","refused":false}]}]}]`
	if _, err := ParseScenarios([]byte(ok)); err != nil {
		t.Fatalf("a well-formed case, with free-form data where data belongs, was refused: %v", err)
	}
	for name, bad := range map[string]string{
		"on the case":        `[{"name":"n","server":[],"steps":[]}]`,
		"on a server":        `[{"name":"n","servers":[{"slug":"a","limits":{}}],"steps":[]}]`,
		"in a catalog":       `[{"name":"n","servers":[{"slug":"a","catalog":{"templates":[]}}],"steps":[]}]`,
		"on a step":          `[{"name":"n","servers":[{"slug":"a"}],"steps":[{"op":"tools/list","expects":[]}]}]`,
		"on a check":         `[{"name":"n","servers":[{"slug":"a"}],"steps":[{"op":"tools/list","expect":[{"path":"client","equal":1}]}]}]`,
		"in the mock script": `[{"name":"n","servers":[{"slug":"a"}],"steps":[{"op":"tools/list","mock":{"progres":[]}}]}]`,
	} {
		if _, err := ParseScenarios([]byte(bad)); err == nil || !strings.Contains(err.Error(), "unknown key") {
			t.Errorf("an unknown key %s was accepted (error: %v)", name, err)
		}
	}
}
