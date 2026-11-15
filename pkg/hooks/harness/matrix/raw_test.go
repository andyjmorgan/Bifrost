package matrix

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/andyjmorgan/Bifrost/pkg/hooks/harness"
	"github.com/andyjmorgan/Bifrost/pkg/hooks/harness/clients"
)

const rawClientName = "matrix-raw-client"

// rawDriver is the bare HTTP client behind the same interface as the SDK
// drivers. It builds 2026-07-28 requests by hand, declares its identity and
// capabilities in every request's _meta, and answers input_required rounds
// from the script as an SDK would. Wires keeps every HTTP reply of the last
// call, so a case can look at what the SDKs hide.
type rawDriver struct {
	url   string
	caps  map[string]any
	seq   int
	Wires []*harness.RawReply
}

func (r *rawDriver) Name() string { return "raw" }

func (r *rawDriver) sdk() clients.SDK {
	return clients.SDK{Name: "raw-http", Version: "1", Language: "go", ProtocolVersion: harness.V2026}
}

// meta is the per-request _meta of a 2026-07-28 client.
func (r *rawDriver) meta() map[string]any {
	caps := r.caps
	if caps == nil {
		caps = map[string]any{}
	}
	return map[string]any{
		"io.modelcontextprotocol/protocolVersion":    harness.V2026,
		"io.modelcontextprotocol/clientInfo":         map[string]any{"name": rawClientName, "version": "1.0.0"},
		"io.modelcontextprotocol/clientCapabilities": caps,
	}
}

// Connect checks that the endpoint answers server/discover, as an SDK does
// when it connects.
func (r *rawDriver) Connect(ctx context.Context, o clients.ConnectOptions) (clients.Reply, error) {
	r.url, r.caps = o.URL, o.Capabilities
	rep, err := harness.RawDo(ctx, r.url, harness.RawRequest{Method: "server/discover", ExactMeta: true, Meta: r.meta()})
	if err != nil {
		return clients.Reply{Error: &clients.Error{Kind: "other", Message: err.Error()}, SDK: r.sdk()}, nil
	}
	if e := rep.Error(); e != nil || rep.Result() == nil {
		return clients.Reply{Error: &clients.Error{Kind: "other", Message: fmt.Sprintf("server/discover: HTTP %d %s %s", rep.Status, asJSON(e), rep.Body)}, SDK: r.sdk()}, nil
	}
	return clients.Reply{SDK: r.sdk()}, nil
}

func (r *rawDriver) Close() error { return nil }

// Discover returns the endpoint's server/discover result.
func (r *rawDriver) Discover(ctx context.Context) (*harness.RawReply, error) {
	return harness.RawDo(ctx, r.url, harness.RawRequest{Method: "server/discover", ExactMeta: true, Meta: r.meta()})
}

// Send makes one request with these params and _meta additions, without any
// input round.
func (r *rawDriver) Send(ctx context.Context, method string, params map[string]any, extraMeta map[string]any) (*harness.RawReply, error) {
	meta := r.meta()
	for k, v := range extraMeta {
		meta[k] = v
	}
	p := map[string]any{}
	for k, v := range params {
		p[k] = v
	}
	return harness.RawDo(ctx, r.url, harness.RawRequest{Method: method, Params: p, ExactMeta: true, Meta: meta})
}

func (r *rawDriver) Call(ctx context.Context, o clients.CallOptions) (clients.Reply, error) {
	r.Wires = nil
	out := clients.Reply{SDK: r.sdk()}
	params := map[string]any{}
	for k, v := range o.Params {
		params[k] = v
	}
	extra := map[string]any{}
	if o.Progress {
		r.seq++
		extra["progressToken"] = fmt.Sprintf("raw-token-%d", r.seq)
	}
	_, canElicit := r.caps["elicitation"]
	script := append([]clients.Answer(nil), o.Elicit...)
	for round := 0; round < 10; round++ {
		rep, err := r.Send(ctx, o.Method, params, extra)
		if err != nil {
			return out, err
		}
		r.Wires = append(r.Wires, rep)
		for _, m := range rep.Messages() {
			if m["method"] == "notifications/progress" {
				p := normalize(m["params"])
				delete(p, "progressToken")
				out.Progress = append(out.Progress, p)
			}
		}
		if !rep.Stream() {
			out.Error = &clients.Error{Kind: "other", Message: fmt.Sprintf("HTTP %d: %s", rep.Status, bytes.TrimSpace(rep.Body))}
			return out, nil
		}
		final := rep.Final()
		if final == nil {
			out.Error = &clients.Error{Kind: "other", Message: "the stream ended without a response"}
			return out, nil
		}
		if e, ok := final["error"].(map[string]any); ok {
			ce := &clients.Error{Kind: "rpc", Data: e["data"]}
			ce.Message, _ = e["message"].(string)
			if n, ok := e["code"].(json.Number); ok {
				f, _ := n.Float64()
				ce.Code = f
			}
			out.Error = ce
			return out, nil
		}
		res := normalize(final["result"])
		out.Result = res
		if res["resultType"] != "input_required" || !canElicit {
			return out, nil
		}
		// Answer each question from the script, in key order, as an SDK's
		// elicitation handler would, and retry with the state.
		reqs, _ := res["inputRequests"].(map[string]any)
		keys := make([]string, 0, len(reqs))
		for k := range reqs {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		responses := map[string]any{}
		for _, k := range keys {
			q, _ := reqs[k].(map[string]any)
			qp, _ := q["params"].(map[string]any)
			out.Elicitations = append(out.Elicitations, qp)
			if len(script) == 0 {
				out.Error = &clients.Error{Kind: "other", Message: "driver: no scripted elicitation answer left"}
				return out, nil
			}
			a := map[string]any{"action": script[0].Action}
			if script[0].Content != nil {
				a["content"] = script[0].Content
			}
			script = script[1:]
			responses[k] = a
		}
		params["inputResponses"] = responses
		params["requestState"] = res["requestState"]
	}
	out.Error = &clients.Error{Kind: "other", Message: "too many input rounds"}
	return out, nil
}
