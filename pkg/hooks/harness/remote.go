package harness

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/andyjmorgan/Bifrost/pkg/hooks"
)

// AddRemote registers a real MCP server, reached by URL, under a slug with
// the given rules. It is AddServer for a server this package did not start,
// such as a reference server built on an official SDK (see the servers
// package), and it forwards through the same code.
//
// protocolVersion is what to speak to the server: V2026, an older version for
// the initialize handshake, or "" to probe (2026-07-28 discovery first, then
// the handshake). The test fails if the server cannot speak what was asked.
//
// A real server records nothing, so tests assert what it received through
// its own replies.
func (g *Gateway) AddRemote(t testing.TB, slug, url, protocolVersion string, rules ...hooks.Rule) *hooks.Server {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	b := &backend{slug: slug, url: url}
	info := hooks.ServerInfo{URL: url}

	if protocolVersion == "" || protocolVersion == V2026 {
		res, err := g.plain(ctx, url, V2026, "server/discover", map[string]any{"_meta": g.triple(nil)})
		if err == nil {
			var d struct {
				SupportedVersions []string       `json:"supportedVersions"`
				Instructions      string         `json:"instructions"`
				Meta              map[string]any `json:"_meta"`
			}
			_ = json.Unmarshal(res, &d)
			for _, v := range d.SupportedVersions {
				if v == V2026 {
					b.version = V2026
				}
			}
			info.Instructions = d.Instructions
			if si, ok := d.Meta["io.modelcontextprotocol/serverInfo"].(map[string]any); ok {
				info.Name, _ = si["name"].(string)
				info.Version, _ = si["version"].(string)
				info.Title, _ = si["title"].(string)
				info.Description, _ = si["description"].(string)
			}
		}
		if protocolVersion == V2026 && b.version != V2026 {
			t.Fatalf("harness: remote server %s at %s does not speak %s: %v", slug, url, V2026, err)
		}
	}
	if b.version == "" {
		res, err := g.initialize(ctx, url, nil)
		if err != nil {
			t.Fatalf("harness: remote server %s at %s answers neither discovery nor initialize: %v", slug, url, err)
		}
		var d struct {
			ProtocolVersion string `json:"protocolVersion"`
			Instructions    string `json:"instructions"`
			ServerInfo      struct {
				Name        string `json:"name"`
				Version     string `json:"version"`
				Title       string `json:"title"`
				Description string `json:"description"`
			} `json:"serverInfo"`
		}
		_ = json.Unmarshal(res, &d)
		if protocolVersion != "" && d.ProtocolVersion != protocolVersion {
			t.Fatalf("harness: remote server %s negotiated %q, want %q", slug, d.ProtocolVersion, protocolVersion)
		}
		b.version = d.ProtocolVersion
		info.Instructions, info.Name, info.Version = d.Instructions, d.ServerInfo.Name, d.ServerInfo.Version
		info.Title, info.Description = d.ServerInfo.Title, d.ServerInfo.Description
	}
	info.ProtocolVersion = b.version

	catalog, err := g.FetchCatalog(ctx, url, b.version)
	if err != nil {
		t.Fatalf("harness: catalog of remote server %s: %v", slug, err)
	}
	catalog = catalog.StripMeta(g.MetaAllow)

	b.server = &hooks.Server{Slug: slug, Entitlement: map[string]any{"slug": slug}, Info: info, Generation: "1", Catalog: catalog}
	b.server.Rules = compile(t, rules)
	g.mu.Lock()
	if _, ok := g.backends[slug]; !ok {
		g.order = append(g.order, slug)
	}
	g.backends[slug] = b
	g.mu.Unlock()
	return b.server
}

// mcpName is the Mcp-Name header of a 2026-07-28 request: the tool or prompt
// name, or the resource URI. Real servers on the official SDKs refuse a
// tools/call, prompts/get or resources/read without it (-32020), so the
// gateway's upstream requests carry it.
func mcpName(version, method string, params any) string {
	if version != V2026 {
		return ""
	}
	p, ok := params.(map[string]any)
	if !ok {
		return ""
	}
	key := ""
	switch method {
	case "tools/call", "prompts/get":
		key = "name"
	case "resources/read", "resources/directory/read":
		key = "uri"
	default:
		return ""
	}
	s, _ := p[key].(string)
	return s
}
