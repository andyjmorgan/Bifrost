package hooks

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// The JSON case runner itself: a case loads strictly, and a check must have
// something to check.

// A misspelt key in a case must stop the case loading. Ignored, it would
// leave a case that tests nothing and passes.
func TestTheCaseLoaderRefusesUnknownKeys(t *testing.T) {
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

// The case runner must not pass a check that had nothing to check.
func TestACaseCheckNeedsSomethingToCheck(t *testing.T) {
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

// TestExpectRequiresPresentValues: missing, null, empty and wrong-type values
// are not interchangeable in len and notContains.
func TestExpectRequiresPresentValues(t *testing.T) {
	zero, one := 0, 1
	out := map[string]any{"empty": []any{}, "null": nil, "num": 3, "list": []any{"a"}}
	cases := []struct {
		ex   Expect
		pass bool
	}{
		{Expect{Path: "empty", Len: &zero}, true},
		{Expect{Path: "missing", Len: &zero}, false},
		{Expect{Path: "null", Len: &zero}, false},
		{Expect{Path: "num", Len: &zero}, false},
		{Expect{Path: "list", Len: &one}, true},
		{Expect{Path: "list", NotContains: "secret"}, true},
		{Expect{Path: "missing", NotContains: "secret"}, false},
		{Expect{Path: "missing[*].name", NotContains: "secret"}, false},
	}
	for _, c := range cases {
		if got := c.ex.check(out, nil) == ""; got != c.pass {
			t.Errorf("%s len=%v notContains=%v: pass=%v, want %v", c.ex.Path, c.ex.Len, c.ex.NotContains, got, c.pass)
		}
	}
}

// A case's `set: []` check passes only on a list that is present and empty:
// not on null, not on a list with elements, and not when the listing failed
// and there is no list at all.
func TestAnEmptySetCheckNeedsAListToCheck(t *testing.T) {
	var ex Expect
	if err := json.Unmarshal([]byte(`{"path":"client.result.tools","set":[]}`), &ex); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, report string
		wantPass     bool
	}{
		{"empty_list_control", `{"client":{"result":{"tools":[]}}}`, true},
		{"nonempty_list_control", `{"client":{"result":{"tools":[{"name":"secret"}]}}}`, false},
		{"null_control", `{"client":{"result":{"tools":null}}}`, false},
		{"error_instead_of_listing", `{"client":{"error":{"code":-32603,"message":"broken"}}}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var report any
			if err := json.Unmarshal([]byte(tc.report), &report); err != nil {
				t.Fatal(err)
			}
			failure := ex.Check(report, nil)
			if (failure == "") != tc.wantPass {
				t.Fatalf("set:[] pass=%v, want %v; diagnostic=%q", failure == "", tc.wantPass, failure)
			}
		})
	}
}

// A case's equals check compares integers exactly: 9007199254740993 is not
// 9007199254740992, although the two are the same float64.
func TestACaseCheckComparesLargeIntegersExactly(t *testing.T) {
	var ex Expect
	if err := json.Unmarshal([]byte(`{"path":"id","equals":9007199254740992}`), &ex); err != nil {
		t.Fatal(err)
	}
	got := map[string]any{"id": json.Number("9007199254740993")}
	if ex.check(got, nil) == "" {
		t.Fatal("scenario assertion treats 9007199254740993 as equal to 9007199254740992")
	}
}

// A case file that parses runs without a panic, whatever it holds.
func FuzzScenarioLoader(f *testing.F) {
	for _, s := range []string{`[]`, `{"name":"x","servers":[],"steps":[]}`, `{"name":"x"}{"name":"y"}`, `[{"servers":[{"slug":"a","rules":[{"hook":"tools/list.post","when":"true","then":{"decision":"'deny'"}}]}],"steps":[{"op":"tools/list"}]}]`} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		if len(s) > 4096 {
			t.Skip()
		}
		ss, err := ParseScenarios([]byte(s))
		if err != nil {
			return
		}
		for _, sc := range ss {
			if sc == nil || len(sc.Steps) > 8 || len(sc.Servers) > 4 {
				continue
			}
			sc.Run(context.Background()) // must not panic
		}
	})
}
