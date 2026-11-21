package hooks

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// JSON numbers: compared by value, kept as written.

// JSON clients spell numbers differently. Equality is by value: 1, 1.0, 1e0
// and 0.1e1 are equal, negative zero equals zero, and every valid spelling is
// decoded as written. A number never equals a string or a boolean.
func TestNumbersAreEqualByValueNotBySpelling(t *testing.T) {
	for _, text := range []string{"1", "1.0", "1e0", "1E+0", "0.1e1"} {
		t.Run(text, func(t *testing.T) {
			if !jsonEqual(int64(1), json.Number(text)) {
				t.Fatalf("numerically equal JSON values became unequal: 1 and %s", text)
			}
			args, err := decodeArgs(json.RawMessage(" \n{\"value\": " + text + "}\t"))
			if err != nil || args["value"] != json.Number(text) {
				t.Fatalf("valid number spelling rejected or altered: %#v, %v", args, err)
			}
		})
	}
	if !jsonEqual(json.Number("-0"), json.Number("0.0")) {
		t.Fatal("JSON negative zero should compare numerically equal to zero")
	}
	if jsonEqual("1", json.Number("1")) || jsonEqual(true, json.Number("1")) {
		t.Fatal("number equality must not coerce strings or booleans")
	}
}

// An integer argument written as 1, 1.0, 1e0, 1E+0 or 0.1e1 passes the same
// schema check and gives the same result when a rule adds to it.
func TestEverySpellingOfAnIntegerGetsTheSameOutcome(t *testing.T) {
	e, r := serverRequest(t, Rule{Hook: "tools/call.pre", When: "true", Then: map[string]string{"request.params.arguments.n": "request.params.arguments.n + 1"}})
	r.Servers[0].Catalog.Tools[0].InputSchema.(map[string]any)["properties"].(map[string]any)["n"] = map[string]any{"type": "integer"}
	for _, number := range []string{"1", "1.0", "1e0", "1E+0", "0.1e1"} {
		t.Run(number, func(t *testing.T) {
			out := e.CallTool(context.Background(), r, &mcp.CallToolParamsRaw{Name: "t", Arguments: json.RawMessage(`{"n":` + number + `}`)})
			if out.Forward == nil {
				b, _ := json.Marshal(out)
				t.Fatalf("valid integral JSON number %s changed policy outcome: %s", number, b)
			}
			args := out.Forward.Params.(*mcp.CallToolParams).Arguments.(map[string]any)
			if !jsonEqual(args["n"], int64(2)) {
				t.Fatalf("wrong patched value for %s: %v", number, args["n"])
			}
		})
	}
}
