// Package toolsearch ranks cached MCP tools with BM25 for the gateway's
// search mode. The corpus is tiny (tens to hundreds of tools) and rebuilt
// per request from the cache, so there is no persistent index.
package toolsearch

import (
	"math"
	"sort"
	"strings"
	"unicode"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Doc is one searchable tool. Namespace text (server slug, name, description,
// instructions) is indexed with every tool of that server so a query naming
// the server pulls in all of its tools.
type Doc struct {
	Namespace    string
	ServerName   string
	ServerDesc   string
	Instructions string
	Tool         *mcp.Tool
}

// Hit is a ranked result.
type Hit struct {
	Doc   Doc
	Score float64
}

const (
	k1           = 1.2
	b            = 0.75
	nsFieldBoost = 0.6 // namespace text counts less than the tool's own text
)

type indexed struct {
	doc  Doc
	tf   map[string]float64 // weighted term frequency
	size float64
}

// Index is a BM25 index over docs.
type Index struct {
	docs   []indexed
	df     map[string]int
	avgLen float64
}

// Build indexes docs.
func Build(docs []Doc) *Index {
	ix := &Index{df: map[string]int{}}
	var total float64
	for _, d := range docs {
		tf := map[string]float64{}
		add := func(text string, w float64) {
			for _, t := range Tokenize(text) {
				tf[t] += w
			}
		}
		add(d.Tool.Name, 2) // the name is the strongest signal
		add(d.Tool.Description, 1)
		add(schemaText(d.Tool.InputSchema), 0.8)
		add(d.Namespace+" "+d.ServerName+" "+d.ServerDesc+" "+d.Instructions, nsFieldBoost)
		var size float64
		for t, n := range tf {
			size += n
			ix.df[t]++
		}
		ix.docs = append(ix.docs, indexed{doc: d, tf: tf, size: size})
		total += size
	}
	if len(ix.docs) > 0 {
		ix.avgLen = total / float64(len(ix.docs))
	}
	return ix
}

// Search ranks docs for query; results with zero score are dropped.
func (ix *Index) Search(query string, limit int) []Hit {
	terms := Tokenize(query)
	if len(terms) == 0 || len(ix.docs) == 0 {
		return nil
	}
	n := float64(len(ix.docs))
	var hits []Hit
	for _, d := range ix.docs {
		var score float64
		for _, t := range terms {
			f := d.tf[t]
			if f == 0 {
				continue
			}
			df := float64(ix.df[t])
			idf := math.Log(1 + (n-df+0.5)/(df+0.5))
			score += idf * (f * (k1 + 1)) / (f + k1*(1-b+b*d.size/ix.avgLen))
		}
		if score > 0 {
			hits = append(hits, Hit{Doc: d.doc, Score: score})
		}
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].Score > hits[j].Score })
	if limit > 0 && len(hits) > limit {
		hits = hits[:limit]
	}
	return hits
}

// Tokenize lowercases, splits on non-alphanumerics and camelCase boundaries,
// and applies light suffix stripping so "notifications" meets "notify".
func Tokenize(s string) []string {
	var out []string
	var cur []rune
	flush := func() {
		if len(cur) == 0 {
			return
		}
		w := stem(strings.ToLower(string(cur)))
		if len(w) > 1 {
			out = append(out, w)
		}
		cur = cur[:0]
	}
	prev := rune(0)
	for _, r := range s {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			// camelCase boundary: lower→Upper
			if unicode.IsUpper(r) && prev != 0 && unicode.IsLower(prev) {
				flush()
			}
			cur = append(cur, r)
		default:
			flush()
		}
		prev = r
	}
	flush()
	return out
}

func stem(w string) string {
	for _, suf := range []string{"ications", "ication", "ations", "ation", "ings", "ing", "ies", "ied", "ers", "er", "es", "ed", "s"} {
		if strings.HasSuffix(w, suf) && len(w)-len(suf) >= 3 {
			w = w[:len(w)-len(suf)]
			break
		}
	}
	if strings.HasSuffix(w, "y") && len(w) > 3 { // notify / notif(ication)
		w = w[:len(w)-1]
	}
	return w
}

// schemaText flattens the parts of a JSON schema a human would read:
// property names, titles, descriptions and enum values.
func schemaText(schema any) string {
	var sb strings.Builder
	var walk func(v any, depth int)
	walk = func(v any, depth int) {
		if depth > 6 {
			return
		}
		switch x := v.(type) {
		case map[string]any:
			for k, vv := range x {
				switch k {
				case "properties":
					if props, ok := vv.(map[string]any); ok {
						for name, p := range props {
							sb.WriteString(name)
							sb.WriteByte(' ')
							walk(p, depth+1)
						}
					}
				case "description", "title":
					if s, ok := vv.(string); ok {
						sb.WriteString(s)
						sb.WriteByte(' ')
					}
				case "enum":
					if arr, ok := vv.([]any); ok {
						for _, e := range arr {
							if s, ok := e.(string); ok {
								sb.WriteString(s)
								sb.WriteByte(' ')
							}
						}
					}
				case "items", "anyOf", "oneOf", "allOf":
					walk(vv, depth+1)
				}
			}
		case []any:
			for _, e := range x {
				walk(e, depth+1)
			}
		}
	}
	walk(schema, 0)
	return sb.String()
}
