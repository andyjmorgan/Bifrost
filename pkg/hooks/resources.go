package hooks

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// readRules runs resources/read.post.result once over the contents of a
// read. Rules see every entry as response.result.contents and may replace
// the list; response.upstream keeps the entries as the server sent them.
// The resource is the one the read named. A rule may drop or change an
// entry but not bring in a URI the read did not return: the entries here
// are already the ones this caller may read, and nothing else may join
// them.
func (e *Engine) readRules(ctx context.Context, r Request, s *Server, v *resView, uri string, roots map[string]any, result map[string]any, contents, sent []any) (out []any, hits []Hit, reason string, denied bool, err error) {
	res := map[string]any{"uri": uri}
	known, listed := v.visibleRes[normalizeURI(uri)]
	if listed {
		res = known
	}
	it := &item{kind: KindResource, id: uri, view: NewView(res), label: uri, bare: !listed}
	resolve := itemResolver(it, v.originalRes[normalizeURI(uri)])
	// sent is the same entries with the server's own bytes, for
	// response.upstream; it differs from contents only where the gateway
	// has already rewritten a skill's file.
	original := celValue(sent)
	cur := contents
	// result is the server's result. Its other fields, such as resultType
	// and _meta, are there for a rule to read on both sides; contents is
	// the one a rule may change.
	with := func(contents any) map[string]any {
		m := make(map[string]any, len(result)+1)
		for k, x := range result {
			m[k] = x
		}
		m["contents"] = contents
		return m
	}
	response := func() map[string]any {
		return map[string]any{"result": with(celValue(cur)), "upstream": map[string]any{"result": with(original)}}
	}
	roots["response"] = response()
	roots[muxVar] = muxContext(r.route(), s.Slug, v.owned())
	o := s.Rules.run(ctx, "resources/read.post.result", uri, resolve, roots, func(key string, val any) error {
		cur = val.([]any) // the one path a rule may set here is contents
		roots["response"] = response()
		return nil
	})
	if o.denied {
		return contents, o.hits, o.reason, true, nil
	}
	if o.err != nil {
		return contents, o.hits, "", false, o.err
	}
	// An entry a rule leaves must be one the read returned, under the same
	// URI exactly as the server wrote it, and keep MCP's form: text and blob
	// are strings.
	returned := map[string]bool{}
	for _, c := range contents {
		cm, _ := c.(map[string]any)
		u, _ := cm["uri"].(string)
		returned[u] = true
	}
	for i, c := range cur {
		cm, ok := c.(map[string]any)
		u, _ := cm["uri"].(string)
		if !ok || !returned[u] {
			return contents, o.hits, "", false, fmt.Errorf("a rule put an entry into contents for %q, which the read did not return", u)
		}
		for _, k := range []string{"text", "blob", "mimeType"} {
			if x, has := cm[k]; has {
				if _, isStr := x.(string); !isStr {
					return contents, o.hits, "", false, fmt.Errorf("contents[%d].%s must be a string", i, k)
				}
			}
		}
	}
	return cur, o.hits, "", false, nil
}

func resourceItems(rs []*mcp.Resource) []map[string]any {
	out := make([]map[string]any, len(rs))
	for i, x := range rs {
		out[i] = toMap(x)
	}
	return out
}

func templateItems(ts []*mcp.ResourceTemplate) []map[string]any {
	out := make([]map[string]any, len(ts))
	for i, x := range ts {
		out[i] = toMap(x)
	}
	return out
}

// ListResources lists every server's exposed resources, slug a-z then URI
// a-z. An entry appears only if the same check a read uses allows it.
func (e *Engine) ListResources(parent context.Context, r Request) (ret Outcome) {
	defer e.recovered(&ret, "resources/list", callRef{method: "resources/list"})
	ctx, cancel := e.ctx(parent)
	defer cancel()
	var o Outcome
	res := &mcp.ListResourcesResult{Resources: []*mcp.Resource{}}
	res.TTLMs, res.CacheScope = e.Limits.ttl(), "private"
	for _, s := range sortedServers(r.Servers) {
		if !e.config(ctx, r, s, "resources", &o) {
			continue
		}
		v := e.resourceView(ctx, r, s)
		o.Hits = append(o.Hits, v.hits...)
		o.Issues = append(o.Issues, prefixed(s.Slug, v.issues)...)
		var uris []string
		for u := range v.visibleRes {
			uris = append(uris, u)
		}
		sort.Strings(uris)
		for _, n := range uris {
			value := v.visibleRes[n]
			upURI, _ := value["uri"].(string)
			if _, ok, _ := e.checkURI(ctx, r, s, v, upURI); !ok {
				continue
			}
			var x mcp.Resource
			b, _ := json.Marshal(value)
			_ = json.Unmarshal(b, &x)
			x.URI = upURI
			if r.mux() {
				x.URI = PrefixURI(s.Slug, x.URI)
			}
			res.Resources = append(res.Resources, &x)
		}
	}
	o.Result = res
	return e.finishOutcome(ctx, o)
}

func deniedUnder(uri string, roots []string) bool {
	for _, root := range roots {
		if underRoot(uri, root) || underRoot(bare(uri), root) {
			return true
		}
	}
	return false
}

// ListTemplates lists every server's exposed resource templates, slug a-z
// then template a-z.
func (e *Engine) ListTemplates(parent context.Context, r Request) (ret Outcome) {
	defer e.recovered(&ret, "resources/templates/list", callRef{method: "resources/templates/list"})
	ctx, cancel := e.ctx(parent)
	defer cancel()
	var o Outcome
	var seen unknowns
	defer func() { ret.Unknown = append(ret.Unknown, seen.list()...) }()
	res := &mcp.ListResourceTemplatesResult{ResourceTemplates: []*mcp.ResourceTemplate{}}
	res.TTLMs, res.CacheScope = e.Limits.ttl(), "private"
	for _, s := range sortedServers(r.Servers) {
		if !e.config(ctx, r, s, "resources", &o) {
			continue
		}
		ls, issues, hits := s.Rules.listItems(ctx, KindTemplate, "resources/templates/list.post", "uriTemplate", templateItems(s.Catalog.Templates), r.context(s), e.Limits.Workers)
		o.Hits = append(o.Hits, hits...)
		o.Issues = append(o.Issues, prefixed(s.Slug, issues)...)
		for _, l := range ls {
			// A template form the gateway cannot parse is listed as it is.
			// Reads under it work; it only matters for denies and routing.
			if ct := compileTemplate(l.upstreamID); ct.unsupported != "" {
				seen.add(UnknownTemplateForm, ct.unsupported, s.Slug, l.upstreamID, "passed", nil)
			}
		}
		for _, l := range ls {
			var x mcp.ResourceTemplate
			b, _ := json.Marshal(l.value)
			_ = json.Unmarshal(b, &x)
			x.URITemplate = l.upstreamID
			if r.mux() {
				x.URITemplate = PrefixURI(s.Slug, x.URITemplate)
			}
			res.ResourceTemplates = append(res.ResourceTemplates, &x)
		}
	}
	o.Result = res
	return e.finishOutcome(ctx, o)
}

// ReadResource runs the request side of resources/read.
func (e *Engine) ReadResource(parent context.Context, r Request, p *mcp.ReadResourceParams) Outcome {
	return e.read(parent, r, p, "resources/read")
}

// ReadDirectory runs the request side of resources/directory/read.
func (e *Engine) ReadDirectory(parent context.Context, r Request, p *mcp.ReadResourceParams) Outcome {
	return e.read(parent, r, p, "resources/directory/read")
}

func (e *Engine) read(parent context.Context, r Request, p *mcp.ReadResourceParams, method string) (ret Outcome) {
	defer e.recovered(&ret, method, readCallRef(method, p))
	ctx, cancel := e.ctx(parent)
	defer cancel()
	s, up, o, rerr := e.route(ctx, r, p.URI, method)
	if rerr != nil {
		o.Error = rerr
		return o
	}
	v := e.resourceView(ctx, r, s)
	o.Hits = append(o.Hits, v.hits...)
	res, ok, hits := e.checkURI(ctx, r, s, v, up)
	o.Hits = append(o.Hits, hits...)
	if !ok {
		o.Error = &RPCError{Code: CodeInvalidParams, Message: "not found: " + p.URI}
		return o
	}
	args := map[string]any{"uri": up}
	it := &item{kind: KindResource, id: up, view: NewView(res), label: up, bare: !v.listedRes[normalizeURI(up)]}
	resolve := itemResolver(it, v.originalRes[normalizeURI(up)])
	params := map[string]any{"uri": up}
	if len(p.Meta) > 0 {
		params["_meta"] = map[string]any(p.Meta)
	}
	c := preCall{
		server: s, method: method, hook: "resources/read.pre", target: up, label: up,
		args: args, state: p.RequestState, answers: p.InputResponses, resolve: resolve, roots: r.context(s).roots(method, params),
		refuse: rpcRefusal, denied: method + " was refused by policy",
		set: func(string, any) error { return nil }, // a read has nothing a rule may set
	}
	pass, goes := e.pre(ctx, r, &o, c)
	if !goes {
		return o
	}
	fp := &mcp.ReadResourceParams{URI: up}
	if !e.send(ctx, r, &o, c, pass, fp, p.Meta, func(meta mcp.Meta, state string, answers mcp.InputResponseMap) {
		fp.Meta, fp.RequestState, fp.InputResponses = meta, state, answers
	}) {
		return o
	}
	o.Call = &CallContext{Server: s, Method: method, Target: up, Exposed: p.URI, Item: res, Bare: !v.listedRes[normalizeURI(up)], Params: params, Args: args, Hash: pass.hash, State: pass.state, Confirmed: pass.confirmed, resolve: resolve, view: v}
	return o
}

// UpstreamReadReply handles the upstream's answer to a forwarded read or
// directory read, using the CallContext read returned with the forward.
func (e *Engine) UpstreamReadReply(parent context.Context, r Request, call *CallContext, p *mcp.ReadResourceParams, raw json.RawMessage) (ret Outcome) {
	defer e.recovered(&ret, "resources/read reply", readCallRef("resources/read", p))
	ctx, cancel := e.ctx(parent)
	defer cancel()
	if call == nil {
		return errOutcome(-32603, "no forwarded request to reply to")
	}
	if len(raw) > e.Limits.maxBytes() {
		return errOutcome(CodeTooLarge, "upstream result exceeds gateway size limit")
	}
	s, up, method := call.Server, call.Target, call.Method
	var o Outcome
	m, ok := decodeExact(raw).(map[string]any)
	if !ok {
		return errOutcome(-32603, "upstream result is not a JSON object")
	}
	var seen unknowns
	defer func() { ret.Unknown = append(ret.Unknown, seen.list()...) }()
	if rt, _ := m["resultType"].(string); rt == "task" {
		return refuseTask(e, ctx, &seen, o, s.Slug, up, m)
	} else if rt != "" && rt != "complete" && rt != "input_required" {
		seen.add(UnknownResultType, rt, s.Slug, up, "passed", m)
	}
	inputUnknowns(&seen, s.Slug, up, m)
	v := call.view
	if v == nil {
		v = e.resourceView(ctx, r, s)
	}
	if isInputRequired(m) {
		if msg, ok := inputAllowed(r, s); !ok {
			o.Issues = append(o.Issues, s.Slug+": "+up+" asked for input and was refused: "+msg)
			o.Error = &RPCError{Code: CodeInvalidParams, Message: msg}
			return e.finishOutcome(ctx, o)
		}
		out, hits, err := e.elicitHook(ctx, r, call, "resources/read.post.elicit", up, replyResolve(call), rawRequests(m))
		o.Hits = append(o.Hits, hits...)
		if err != nil {
			o.Error = &RPCError{Code: CodeInvalidParams, Message: err.Error()}
			return e.finishOutcome(ctx, o)
		}
		upState, _ := m["requestState"].(string)
		st, err := e.wrapState(r, s, call, p.RequestState, upState, out)
		if err != nil {
			o.Error = &RPCError{Code: CodeInvalidParams, Message: err.Error()}
			return e.finishOutcome(ctx, o)
		}
		o.Result = InputRequired{InputRequests: out, RequestState: st}
		return e.finishOutcome(ctx, o)
	}
	m = copyMap(m)
	if method == "resources/directory/read" {
		kept := []any{}
		children, _ := m["resources"].([]any)
		for _, c := range children {
			cm, _ := c.(map[string]any)
			cu, _ := cm["uri"].(string)
			if cu == "" || nonCanonical(cu) || !underRoot(cu, up) || normalizeURI(cu) == normalizeURI(up) {
				o.Issues = append(o.Issues, "dropped directory entry outside "+up+": "+cu)
				continue
			}
			if _, ok, _ := e.checkURI(ctx, r, s, v, cu); !ok {
				continue
			}
			kept = append(kept, cm)
		}
		m["resources"] = kept
	} else {
		contents, _ := m["contents"].([]any)
		kept, sent := []any{}, []any{}
		// fixed holds the entries a skill's manifest vouches for, by URI.
		// Rules may refuse the read or drop such an entry, but its bytes
		// stay as the manifest says.
		fixed := map[string]map[string]any{}
		for _, c := range contents {
			cm, isMap := c.(map[string]any)
			cu, isStr := cm["uri"].(string)
			if _, has := cm["uri"]; !isMap || (has && !isStr) {
				// Not an entry MCP describes. It is not made into one.
				o.Issues = append(o.Issues, "dropped content that is not a resource entry")
				continue
			}
			if cu == "" {
				cu = up
				cm = copyMap(cm)
				cm["uri"] = up
			}
			if nonCanonical(cu) {
				o.Issues = append(o.Issues, "dropped content with a non-canonical URI: "+cu)
				continue
			}
			if normalizeURI(cu) != normalizeURI(up) {
				if _, ok, _ := e.checkURI(ctx, r, s, v, cu); !ok {
					o.Issues = append(o.Issues, "dropped content for "+cu+": not readable by this caller")
					continue
				}
			}
			server := cm
			// A skill vouches for the bytes of its SKILL.md and of the files
			// its manifest lists. Applied: the plan already made that file's
			// rewrite. Skipped or rejected: the server's bytes are served.
			// Another file under a skill's folder is an ordinary entry, and
			// a rule's change to it stands.
			if sk, vouched := v.vouchedBy(cu); vouched {
				if _, again := fixed[normalizeURI(cu)]; again {
					// A file has one set of bytes. A second entry for it
					// could come back in place of the first after a rule
					// had dropped it.
					o.Issues = append(o.Issues, "dropped a second entry for "+cu+": its skill vouches for one")
					continue
				}
				plan := e.planSkill(ctx, r, s, v, sk)
				if t, changed := plan.files[cu]; plan.status == "applied" && changed {
					if _, isText := cm["text"].(string); isText {
						cm = copyMap(cm)
						cm["text"] = t
					}
				}
				fixed[normalizeURI(cu)] = cm
			}
			kept, sent = append(kept, cm), append(sent, server)
		}
		out, hits, reason, denied, err := e.readRules(ctx, r, s, v, up, call.roots(r), m, kept, sent)
		o.Hits = append(o.Hits, hits...)
		if denied {
			if reason == "" {
				reason = "the read result was refused by policy"
			}
			o.Error = &RPCError{Code: CodeInvalidParams, Message: reason}
			return e.finishOutcome(ctx, o)
		}
		if err != nil {
			o.Error = &RPCError{Code: CodeInvalidParams, Message: "the read result could not be checked"}
			return e.finishOutcome(ctx, o)
		}
		kept = make([]any, 0, len(out))
		for _, c := range out {
			cm := c.(map[string]any)
			cu, _ := cm["uri"].(string)
			if f, ok := fixed[normalizeURI(cu)]; ok {
				if !reflect.DeepEqual(cm, f) {
					o.Issues = append(o.Issues, "a rule's change to "+cu+" was not applied: its skill vouches for its bytes")
				}
				cm = f
			}
			kept = append(kept, cm)
		}
		m["contents"] = kept
	}
	if r.mux() {
		m = prefixResultURIs(m, s.Slug)
	}
	m["ttlMs"], m["cacheScope"] = e.Limits.ttl(), "private"
	if call.State != nil || len(call.Confirmed) > 0 {
		// This read was let through by a confirmation or after a question.
		// A client that cached it would serve the next read itself, and the
		// user would not be asked again. Tell clients not to cache it.
		m["ttlMs"] = 0
	}
	o.Result = complete(m)
	return e.finishOutcome(ctx, o)
}
