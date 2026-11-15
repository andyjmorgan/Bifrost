package skills

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"testing"
)

var wantSkillURIs = []string{
	BillingRefundsSkill,
	SupportRefundsSkill,
	NotebooksSkill,
	DailySkill,
	ChartsSkill,
	WidgetsSkill,
}

func init() { slices.Sort(wantSkillURIs) }

func digestByHand(b []byte) string {
	h := sha256.New()
	h.Write(b)
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}

func TestDigestHelper(t *testing.T) {
	// Vector from the spec's example (invoice.md).
	if got := Digest([]byte("# Invoice\n\nCustomer:\nAmount:\n")); got != "sha256:61f4ea6d2c75fde1b4977219e7e3107d491c3c26aefb6686e84d6281c088d9ee" {
		t.Fatalf("Digest = %s", got)
	}
}

func TestDiscoverDeclaresExtension(t *testing.T) {
	s, c := newTestServer(t, Options{})
	res := c.ok("server/discover", nil, nil)
	caps := res["capabilities"].(map[string]any)
	if _, ok := caps["resources"].(map[string]any); !ok {
		t.Fatalf("resources capability missing: %v", caps)
	}
	ext := caps["extensions"].(map[string]any)[ExtensionID].(map[string]any)
	if ext["directoryRead"] != true {
		t.Fatalf("directoryRead = %v", ext["directoryRead"])
	}
	if res["cacheScope"] != "public" || res["ttlMs"] == nil {
		t.Fatalf("cacheable fields missing: %v", res)
	}
	if !slices.Contains(toStrings(res["supportedVersions"]), ProtocolVersion) {
		t.Fatalf("supportedVersions = %v", res["supportedVersions"])
	}

	init := c.ok("initialize", map[string]any{"protocolVersion": "2025-11-25", "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "old", "version": "1"}}, nil)
	if init["protocolVersion"] != "2025-11-25" {
		t.Fatalf("initialize protocolVersion = %v", init["protocolVersion"])
	}
	if _, ok := init["capabilities"].(map[string]any)["extensions"].(map[string]any)[ExtensionID]; !ok {
		t.Fatal("initialize does not declare the extension")
	}

	// Runtime toggle off.
	s.SetSkillsExtension(false)
	res = c.ok("server/discover", nil, nil)
	if _, ok := res["capabilities"].(map[string]any)["extensions"]; ok {
		t.Fatal("extension still declared after SetSkillsExtension(false)")
	}
	r := c.do("skills/list", nil, nil)
	if r.Status != http.StatusNotFound || r.Error == nil || r.Error.Code != CodeMethodNotFound {
		t.Fatalf("skills/list with extension off: %+v", r)
	}
	if code := c.errCode("resources/directory/read", map[string]any{"uri": "skill://widgets"}); code != CodeMethodNotFound {
		t.Fatalf("directory read with extension off: %d", code)
	}
	// Resources keep working.
	c.readBytes(t, NotebooksSkill)
}

func TestOptionsDisable(t *testing.T) {
	_, c := newTestServer(t, Options{DisableSkillsExtension: true})
	res := c.ok("server/discover", nil, nil)
	if _, ok := res["capabilities"].(map[string]any)["extensions"]; ok {
		t.Fatal("extension declared with DisableSkillsExtension")
	}
	_, c = newTestServer(t, Options{DisableDirectoryRead: true})
	res = c.ok("server/discover", nil, nil)
	ext := res["capabilities"].(map[string]any)["extensions"].(map[string]any)[ExtensionID].(map[string]any)
	if len(ext) != 0 {
		t.Fatalf("extension settings = %v, want {}", ext)
	}
	if code := c.errCode("resources/directory/read", map[string]any{"uri": "skill://widgets"}); code != CodeMethodNotFound {
		t.Fatalf("directory read without directoryRead: %d", code)
	}
}

func toStrings(v any) []string {
	var out []string
	for _, x := range v.([]any) {
		out = append(out, x.(string))
	}
	return out
}

func TestSkillsListManifests(t *testing.T) {
	s, c := newTestServer(t, Options{})
	var res listSkillsRes
	gen := c.ok("skills/list", nil, &res)
	if gen["cacheScope"] != "public" || gen["ttlMs"] == nil {
		t.Fatalf("skills/list missing ttlMs/cacheScope: %v", gen)
	}
	var got []string
	for _, sk := range res.Skills {
		got = append(got, sk.URI)
	}
	if !slices.Equal(got, wantSkillURIs) {
		t.Fatalf("skills = %v\nwant %v", got, wantSkillURIs)
	}

	all := s.URIs()
	for _, sk := range res.Skills {
		root := strings.TrimSuffix(sk.URI, "/SKILL.md")
		if last := root[strings.LastIndex(root, "/")+1:]; last != sk.Name() {
			t.Errorf("%s: last segment %q != name %q", sk.URI, last, sk.Name())
		}
		if _, ok := sk.Frontmatter["description"].(string); !ok {
			t.Errorf("%s: no description", sk.URI)
		}
		// Frontmatter equals the file's frontmatter (read over the wire).
		body, mt := c.readBytes(t, sk.URI)
		if mt != "text/markdown" {
			t.Errorf("%s: mimeType %s", sk.URI, mt)
		}
		fm, err := ParseFrontmatter(body)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(fm, sk.Frontmatter) {
			t.Errorf("%s: frontmatter %v != file %v", sk.URI, sk.Frontmatter, fm)
		}

		if sk.URI == DailySkill {
			if !sk.IsDynamic() {
				t.Errorf("daily skill resources = %s, want \"dynamic\"", sk.Resources)
			}
			continue
		}
		files := sk.Files()
		if files == nil {
			t.Fatalf("%s: resources = %s", sk.URI, sk.Resources)
		}
		// Complete: exactly the files under the root, each once, SKILL.md included.
		var listed, want []string
		for _, f := range files {
			listed = append(listed, f.URI)
		}
		for _, u := range all {
			if strings.HasPrefix(u, root+"/") {
				want = append(want, u)
			}
		}
		if !slices.Equal(listed, want) || !slices.Contains(listed, sk.URI) {
			t.Errorf("%s: manifest %v, want %v", sk.URI, listed, want)
		}
		// Digests and sizes recomputed from bytes read over the wire.
		for _, f := range files {
			b, _ := c.readBytes(t, f.URI)
			if f.Size != len(b) {
				t.Errorf("%s: size %d, read %d bytes", f.URI, f.Size, len(b))
			}
			if f.Digest != digestByHand(b) {
				t.Errorf("%s: digest %s, want %s", f.URI, f.Digest, digestByHand(b))
			}
		}
	}

	byURI := map[string]Skill{}
	for _, sk := range res.Skills {
		byURI[sk.URI] = sk
	}
	// Enclosing skill lists the nested skill's files.
	widgetFiles := uris(byURI[WidgetsSkill].Files())
	for _, u := range []string{WidgetsSkill, WidgetsTimeseries, ChartsSkill, ChartsPalette} {
		if !slices.Contains(widgetFiles, u) {
			t.Errorf("widgets manifest lacks %s", u)
		}
	}
	if got := uris(byURI[ChartsSkill].Files()); !slices.Equal(got, []string{ChartsSkill, ChartsPalette}) {
		t.Errorf("charts manifest = %v", got)
	}
	// Same name, different skills.
	b, sup := byURI[BillingRefundsSkill], byURI[SupportRefundsSkill]
	if b.Name() != "refunds" || sup.Name() != "refunds" || mustJSON(b.Frontmatter) == mustJSON(sup.Frontmatter) {
		t.Errorf("refunds pair: %v / %v", b.Frontmatter, sup.Frontmatter)
	}
	// Frontmatter passes extra fields through verbatim.
	md := byURI[NotebooksSkill].Frontmatter["metadata"].(map[string]any)
	if md["version"] != "1.2" || len(md["tags"].([]any)) != 2 || byURI[NotebooksSkill].Frontmatter["license"] != "Apache-2.0" {
		t.Errorf("notebooks frontmatter = %v", byURI[NotebooksSkill].Frontmatter)
	}
	// Notebooks body carries the references the gateway tests rely on.
	nb, _ := s.File(NotebooksSkill)
	for _, ref := range []string{"references/advanced-analysis.md", WidgetsSkill, WidgetsTimeseries} {
		if !bytes.Contains(nb, []byte(ref)) {
			t.Errorf("notebooks SKILL.md does not mention %s", ref)
		}
	}
}

func uris(rs []SkillResource) []string {
	var out []string
	for _, r := range rs {
		out = append(out, r.URI)
	}
	return out
}

func TestSkillsGet(t *testing.T) {
	_, c := newTestServer(t, Options{Unlisted: []string{ChartsSkill}})
	listed := c.listAllSkills(t)
	if slices.ContainsFunc(listed, func(s Skill) bool { return s.URI == ChartsSkill }) {
		t.Fatal("unlisted charts skill appears in skills/list")
	}
	byURI := map[string]Skill{}
	for _, sk := range listed {
		byURI[sk.URI] = sk
	}
	for _, u := range wantSkillURIs {
		var res struct {
			Skill Skill `json:"skill"`
			TTLMs *int  `json:"ttlMs"`
		}
		gen := c.ok("skills/get", map[string]any{"uri": u}, &res)
		if gen["cacheScope"] == nil || res.TTLMs == nil {
			t.Errorf("%s: skills/get missing ttlMs/cacheScope", u)
		}
		if _, has := gen["nextCursor"]; has {
			t.Errorf("%s: skills/get carries a cursor", u)
		}
		if res.Skill.URI != u {
			t.Errorf("get %s returned %s", u, res.Skill.URI)
		}
		if l, ok := byURI[u]; ok {
			if mustJSON(l) != mustJSON(res.Skill) {
				t.Errorf("%s: get %s != list %s", u, mustJSON(res.Skill), mustJSON(l))
			}
		} else if u != ChartsSkill {
			t.Errorf("%s missing from listing", u)
		}
		if u == DailySkill && (!res.Skill.IsDynamic() || *res.TTLMs != 0) {
			t.Errorf("daily: resources %s ttl %d", res.Skill.Resources, *res.TTLMs)
		}
	}
	for _, bad := range []string{
		"skill://acme/billing/chargebacks/SKILL.md", // unknown skill
		NotebooksAdvanced, // a file, not a SKILL.md
		"skill://widgets", // a directory
		ReadmeResource,
		"",
	} {
		if code := c.errCode("skills/get", map[string]any{"uri": bad}); code != CodeInvalidParams {
			t.Errorf("skills/get %q: code %d, want -32602", bad, code)
		}
	}
	if code := c.errCode("skills/get", nil); code != CodeInvalidParams {
		t.Errorf("skills/get without uri: %d", code)
	}
}

func TestResourcesReadExactBytes(t *testing.T) {
	_, c := newTestServer(t, Options{})
	for u, want := range DefaultFiles() {
		got, _ := c.readBytes(t, u)
		if !bytes.Equal(got, want) {
			t.Errorf("%s: read %q, want %q", u, got, want)
		}
	}
	for _, bad := range []string{"skill://notebooks/nope.md", "skill://ghost/SKILL.md", "file:///nope", "skill://widgets/references"} {
		if code := c.errCode("resources/read", map[string]any{"uri": bad}); code != CodeInvalidParams {
			t.Errorf("read %s: code %d, want -32602", bad, code)
		}
	}
	// Dynamic content changes between reads and is not cacheable.
	var r1, r2 readRes
	c.ok("resources/read", map[string]any{"uri": DailyLatest}, &r1)
	c.ok("resources/read", map[string]any{"uri": DailyLatest}, &r2)
	if *r1.Contents[0].Text == *r2.Contents[0].Text || r1.TTLMs != 0 {
		t.Errorf("dynamic read: %q / %q ttl %d", *r1.Contents[0].Text, *r2.Contents[0].Text, r1.TTLMs)
	}
}

func TestResourcesListAndTemplate(t *testing.T) {
	_, c := newTestServer(t, Options{})
	var lr struct {
		Resources []Resource `json:"resources"`
	}
	gen := c.ok("resources/list", nil, &lr)
	if gen["ttlMs"] == nil || gen["cacheScope"] == nil {
		t.Fatal("resources/list not cacheable")
	}
	got := map[string]Resource{}
	for _, r := range lr.Resources {
		got[r.URI] = r
	}
	nb, ok := got[NotebooksSkill]
	if !ok || nb.Name != "notebooks" || nb.MIMEType != "text/markdown" || nb.Description == "" {
		t.Errorf("notebooks SKILL.md resource = %+v", nb)
	}
	if _, ok := got[ReadmeResource]; !ok {
		t.Errorf("README missing: %v", lr.Resources)
	}
	if _, ok := got[WidgetsTimeseries]; ok {
		t.Errorf("timeseries should be reachable only through the template")
	}

	var tr struct {
		ResourceTemplates []ResourceTemplate `json:"resourceTemplates"`
	}
	gen = c.ok("resources/templates/list", nil, &tr)
	if gen["ttlMs"] == nil || len(tr.ResourceTemplates) != 1 || tr.ResourceTemplates[0].URITemplate != SkillTemplate {
		t.Fatalf("templates = %+v", tr)
	}
	// The template resolves files of every skill, listed or not.
	for _, u := range []string{WidgetsTimeseries, ChartsPalette, BillingRefundsEmail, SupportRefundsSkill, DailySkill} {
		if b, _ := c.readBytes(t, u); len(b) == 0 {
			t.Errorf("%s empty", u)
		}
	}
}

type dirRes struct {
	Resources  []Resource `json:"resources"`
	NextCursor string     `json:"nextCursor"`
}

func TestDirectoryRead(t *testing.T) {
	_, c := newTestServer(t, Options{})
	type kid struct{ uri, mime string }
	cases := map[string][]kid{
		"skill://widgets": {
			{WidgetsSkill, "text/markdown"},
			{"skill://widgets/charts", DirectoryMIMEType},
			{"skill://widgets/references", DirectoryMIMEType},
		},
		"skill://widgets/references":    {{WidgetsTimeseries, "text/markdown"}},
		"skill://widgets/charts":        {{ChartsSkill, "text/markdown"}, {"skill://widgets/charts/assets", DirectoryMIMEType}},
		"skill://widgets/charts/assets": {{ChartsPalette, "application/json"}},
		"skill://acme":                  {{"skill://acme/billing", DirectoryMIMEType}, {"skill://acme/support", DirectoryMIMEType}},
		"skill://reports/daily":         {{DailySkill, "text/markdown"}, {"skill://reports/daily/data", DirectoryMIMEType}},
		"skill://notebooks":             {{NotebooksSkill, "text/markdown"}, {"skill://notebooks/references", DirectoryMIMEType}},
	}
	for dir, want := range cases {
		var res dirRes
		gen := c.ok("resources/directory/read", map[string]any{"uri": dir}, &res)
		if gen["ttlMs"] == nil || gen["cacheScope"] == nil {
			t.Errorf("%s: not cacheable", dir)
		}
		var got []kid
		for _, r := range res.Resources {
			if strings.HasSuffix(r.URI, "/") {
				t.Errorf("%s: child %s has trailing slash", dir, r.URI)
			}
			if r.Name == "" {
				t.Errorf("%s: child %s has no name", dir, r.URI)
			}
			got = append(got, kid{r.URI, r.MIMEType})
		}
		slices.SortFunc(want, func(a, b kid) int { return strings.Compare(a.uri, b.uri) })
		if !slices.Equal(got, want) {
			t.Errorf("%s: children %v, want %v", dir, got, want)
		}
	}
	for _, bad := range []string{
		"skill://widgets/",      // trailing slash is not the directory URI
		WidgetsSkill,            // a file
		"skill://nope",          // does not exist
		"skill://widgets/refer", // partial segment
		"skill://",              // scheme root
		"",
	} {
		if code := c.errCode("resources/directory/read", map[string]any{"uri": bad}); code != CodeInvalidParams {
			t.Errorf("directory read %q: code %d, want -32602", bad, code)
		}
	}
}

func TestPaginationKeepsEntriesAtomic(t *testing.T) {
	_, full := newTestServer(t, Options{})
	var whole listSkillsRes
	full.ok("skills/list", nil, &whole)
	if whole.NextCursor != "" {
		t.Fatal("unpaginated listing has a cursor")
	}

	for _, size := range []int{1, 2, 4} {
		_, c := newTestServer(t, Options{PageSize: size})
		var pages int
		var all []Skill
		cursor := ""
		for {
			p := map[string]any{}
			if cursor != "" {
				p["cursor"] = cursor
			}
			var res listSkillsRes
			c.ok("skills/list", p, &res)
			pages++
			if len(res.Skills) > size {
				t.Fatalf("page of %d entries with PageSize %d", len(res.Skills), size)
			}
			all = append(all, res.Skills...)
			if res.NextCursor == "" {
				break
			}
			cursor = res.NextCursor
		}
		if want := (len(whole.Skills) + size - 1) / size; pages != want {
			t.Errorf("size %d: %d pages, want %d", size, pages, want)
		}
		// Every entry arrives whole: same manifest as the unpaginated one.
		if mustJSON(all) != mustJSON(whole.Skills) {
			t.Errorf("size %d: paged entries differ from the unpaginated listing", size)
		}
		if code := c.errCode("skills/list", map[string]any{"cursor": "not-a-cursor"}); code != CodeInvalidParams {
			t.Errorf("bad cursor: %d", code)
		}
	}

	// Directory reads paginate too.
	_, c := newTestServer(t, Options{PageSize: 1})
	var kids []string
	cursor := ""
	for {
		p := map[string]any{"uri": "skill://widgets"}
		if cursor != "" {
			p["cursor"] = cursor
		}
		var res dirRes
		c.ok("resources/directory/read", p, &res)
		for _, r := range res.Resources {
			kids = append(kids, r.URI)
		}
		if res.NextCursor == "" {
			break
		}
		cursor = res.NextCursor
	}
	if len(kids) != 3 {
		t.Errorf("paged directory children = %v", kids)
	}
}

func TestChangeFileBetweenListAndRead(t *testing.T) {
	s, c := newTestServer(t, Options{})
	before := c.listAllSkills(t)
	var oldDigest string
	for _, sk := range before {
		if sk.URI == NotebooksSkill {
			for _, f := range sk.Files() {
				if f.URI == NotebooksAdvanced {
					oldDigest = f.Digest
				}
			}
		}
	}
	updated := []byte("# Advanced analysis\n\nRewritten.\n")
	s.SetFile(NotebooksAdvanced, updated)
	got, _ := c.readBytes(t, NotebooksAdvanced)
	if !bytes.Equal(got, updated) || Digest(got) == oldDigest {
		t.Fatalf("read after SetFile = %q", got)
	}
	var res struct {
		Skill Skill `json:"skill"`
	}
	c.ok("skills/get", map[string]any{"uri": NotebooksSkill}, &res)
	if d, _, _ := s.FileDigest(NotebooksAdvanced); !slices.Contains(uris(res.Skill.Files()), NotebooksAdvanced) || d != Digest(updated) {
		t.Fatalf("FileDigest = %s", d)
	}
	for _, f := range res.Skill.Files() {
		if f.URI == NotebooksAdvanced && (f.Digest != Digest(updated) || f.Size != len(updated)) {
			t.Fatalf("refreshed entry = %+v", f)
		}
	}

	// Adding a file extends the manifest; directory read sees it.
	s.SetFile("skill://notebooks/references/new.md", []byte("new\n"))
	c.ok("skills/get", map[string]any{"uri": NotebooksSkill}, &res)
	if !slices.Contains(uris(res.Skill.Files()), "skill://notebooks/references/new.md") {
		t.Fatal("new file not in manifest")
	}
	// Breaking the name drops the skill.
	s.SetFile(SupportRefundsSkill, []byte("---\nname: other\ndescription: x\n---\n"))
	if code := c.errCode("skills/get", map[string]any{"uri": SupportRefundsSkill}); code != CodeInvalidParams {
		t.Fatalf("mismatched-name skill: %d", code)
	}
	s.RemoveFile(NotebooksAdvanced)
	if code := c.errCode("resources/read", map[string]any{"uri": NotebooksAdvanced}); code != CodeInvalidParams {
		t.Fatalf("removed file: %d", code)
	}
}

func TestTransportValidationAndRecording(t *testing.T) {
	s, c := newTestServer(t, Options{})
	r := c.do("skills/list", nil, map[string]string{"MCP-Protocol-Version": "2025-11-25"})
	if r.Status != http.StatusBadRequest || r.Error == nil || r.Error.Code != CodeHeaderMismatch {
		t.Errorf("version header mismatch: %+v", r)
	}
	r = c.do("skills/list", nil, map[string]string{"Mcp-Method": "tools/list"})
	if r.Status != http.StatusBadRequest || r.Error == nil || r.Error.Code != CodeHeaderMismatch {
		t.Errorf("method header mismatch: %+v", r)
	}
	r = c.do("resources/read", map[string]any{"uri": NotebooksSkill}, map[string]string{"Mcp-Name": "skill://other/SKILL.md"})
	if r.Status != http.StatusBadRequest || r.Error == nil || r.Error.Code != CodeHeaderMismatch {
		t.Errorf("name header mismatch: %+v", r)
	}
	meta := reqMeta()
	meta[MetaProtocolVersion] = "1999-01-01"
	r = c.do("skills/list", map[string]any{"_meta": meta}, map[string]string{"MCP-Protocol-Version": "1999-01-01"})
	if r.Status != http.StatusBadRequest || r.Error == nil || r.Error.Code != CodeUnsupportedProtocolVersion {
		t.Errorf("unsupported version: %+v", r)
	}
	r = c.do("tools/list", nil, nil)
	if r.Status != http.StatusNotFound || r.Error == nil || r.Error.Code != CodeMethodNotFound {
		t.Errorf("unknown method: %+v", r)
	}

	// Notification: 202, recorded.
	res, err := http.Post(c.url, "application/json", strings.NewReader(`{"jsonrpc":"2.0","method":"notifications/initialized"}`))
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusAccepted {
		t.Errorf("notification status %d", res.StatusCode)
	}

	s.ResetRequests()
	c.ok("skills/get", map[string]any{"uri": WidgetsSkill}, nil)
	reqs := s.Requests()
	if len(reqs) != 1 {
		t.Fatalf("recorded %d requests", len(reqs))
	}
	got := reqs[0]
	if got.Method != "skills/get" || got.URI() != WidgetsSkill || got.Meta()[MetaProtocolVersion] != ProtocolVersion || got.Header.Get("Mcp-Method") != "skills/get" {
		t.Errorf("recorded %+v", got)
	}
	var p map[string]any
	if err := json.Unmarshal(got.Params, &p); err != nil || p["uri"] != WidgetsSkill {
		t.Errorf("raw params %s", got.Params)
	}
	if len(s.RequestsFor("skills/get")) != 1 || len(s.RequestsFor("skills/list")) != 0 {
		t.Error("RequestsFor")
	}
}
