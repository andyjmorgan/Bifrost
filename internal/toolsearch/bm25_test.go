package toolsearch

import (
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func doc(ns, name, desc string, props ...string) Doc {
	p := map[string]any{}
	for _, k := range props {
		p[k] = map[string]any{"type": "string"}
	}
	return Doc{Namespace: ns, ServerName: ns, Tool: &mcp.Tool{Name: name, Description: desc, InputSchema: map[string]any{"type": "object", "properties": p}}}
}

func TestTokenize(t *testing.T) {
	got := Tokenize("send_telegram_notification webFetch Notifications")
	want := map[string]bool{"send": true, "telegram": true, "notif": true, "web": true, "fetch": true}
	for _, g := range got {
		delete(want, g)
	}
	if len(want) != 0 {
		t.Fatalf("missing tokens %v in %v", want, got)
	}
}

func TestSearchRanksNameAndNamespace(t *testing.T) {
	ix := Build([]Doc{
		doc("telegram", "send_telegram_notification", "Send a Telegram message to the owner", "text"),
		doc("obsidian", "create_note", "Create a note in the vault", "path", "content"),
		doc("obsidian", "search_notes", "Full-text search across notes", "query"),
		doc("websearch", "brave_web_search", "Search the web", "query", "count"),
	})
	hits := ix.Search("notify me", 5)
	if len(hits) == 0 || hits[0].Doc.Tool.Name != "send_telegram_notification" {
		t.Fatalf("notify: got %+v", names(hits))
	}
	hits = ix.Search("obsidian", 5)
	if len(hits) != 2 {
		t.Fatalf("namespace query should pull both obsidian tools, got %v", names(hits))
	}
	hits = ix.Search("search", 5)
	if len(hits) < 2 || (hits[0].Doc.Tool.Name != "search_notes" && hits[0].Doc.Tool.Name != "brave_web_search") {
		t.Fatalf("search: got %v", names(hits))
	}
	if h := ix.Search("zzz nothing", 5); len(h) != 0 {
		t.Fatalf("expected no hits, got %v", names(h))
	}
}

func names(h []Hit) []string {
	out := make([]string, len(h))
	for i, x := range h {
		out[i] = x.Doc.Tool.Name
	}
	return out
}
