package skills

import (
	"bytes"
	"context"
	"net/http/httptest"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type sdkListSkillsParams struct {
	mcp.ParamsBase
	Cursor string `json:"cursor,omitempty"`
}

type sdkListSkillsResult struct {
	mcp.ResultBase
	ResultType string  `json:"resultType"`
	Skills     []Skill `json:"skills"`
	NextCursor string  `json:"nextCursor,omitempty"`
	TTLMs      *int    `json:"ttlMs"`
	CacheScope string  `json:"cacheScope"`
}

type sdkURIParams struct {
	mcp.ParamsBase
	URI    string `json:"uri"`
	Cursor string `json:"cursor,omitempty"`
}

type sdkGetSkillResult struct {
	mcp.ResultBase
	ResultType string `json:"resultType"`
	Skill      Skill  `json:"skill"`
	TTLMs      *int   `json:"ttlMs"`
	CacheScope string `json:"cacheScope"`
}

type sdkDirResult struct {
	mcp.ResultBase
	ResultType string     `json:"resultType"`
	Resources  []Resource `json:"resources"`
	NextCursor string     `json:"nextCursor,omitempty"`
}

// TestGoSDKClient drives the server with the go-sdk v1.8.0 client: the
// standard resources methods through its API, skills/* and
// resources/directory/read through CallCustomMethod.
func TestGoSDKClient(t *testing.T) {
	s := MustNew(Options{})
	ts := httptest.NewServer(s)
	defer ts.Close()
	ctx := context.Background()

	client := mcp.NewClient(&mcp.Implementation{Name: "sdk-test", Version: "1.0.0"}, nil)
	for _, m := range []string{"skills/list", "skills/get", "resources/directory/read"} {
		var err error
		switch m {
		case "skills/list":
			err = mcp.AddSendingCustomMethod[*sdkListSkillsParams, *sdkListSkillsResult](client, m)
		case "skills/get":
			err = mcp.AddSendingCustomMethod[*sdkURIParams, *sdkGetSkillResult](client, m)
		default:
			err = mcp.AddSendingCustomMethod[*sdkURIParams, *sdkDirResult](client, m)
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	cs, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: ts.URL}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()

	ir := cs.InitializeResult()
	ext, ok := ir.Capabilities.Extensions[ExtensionID].(map[string]any)
	if !ok || ext["directoryRead"] != true {
		t.Fatalf("extensions = %#v", ir.Capabilities.Extensions)
	}
	if ir.Capabilities.Resources == nil {
		t.Fatal("resources capability missing")
	}

	lr, err := cs.ListResources(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(lr.Resources) != len(DefaultListedResources) {
		t.Errorf("ListResources = %d resources", len(lr.Resources))
	}
	tr, err := cs.ListResourceTemplates(ctx, nil)
	if err != nil || len(tr.ResourceTemplates) != 1 || tr.ResourceTemplates[0].URITemplate != SkillTemplate {
		t.Fatalf("templates %+v %v", tr, err)
	}
	rr, err := cs.ReadResource(ctx, &mcp.ReadResourceParams{URI: WidgetsTimeseries})
	if err != nil {
		t.Fatal(err)
	}
	want, _ := s.File(WidgetsTimeseries)
	if len(rr.Contents) != 1 || !bytes.Equal([]byte(rr.Contents[0].Text), want) {
		t.Fatalf("ReadResource = %+v", rr.Contents)
	}
	if _, err := cs.ReadResource(ctx, &mcp.ReadResourceParams{URI: "skill://ghost/SKILL.md"}); err == nil {
		t.Fatal("ReadResource of unknown URI succeeded")
	}

	ls, err := mcp.CallCustomMethod[*sdkListSkillsParams, *sdkListSkillsResult](ctx, cs, "skills/list", &sdkListSkillsParams{})
	if err != nil {
		t.Fatal(err)
	}
	if ls.ResultType != "complete" || ls.TTLMs == nil || ls.CacheScope == "" || len(ls.Skills) != len(wantSkillURIs) {
		t.Fatalf("skills/list = %+v", ls)
	}
	gs, err := mcp.CallCustomMethod[*sdkURIParams, *sdkGetSkillResult](ctx, cs, "skills/get", &sdkURIParams{URI: ChartsSkill})
	if err != nil || gs.Skill.Name() != "charts" {
		t.Fatalf("skills/get = %+v %v", gs, err)
	}
	if _, err := mcp.CallCustomMethod[*sdkURIParams, *sdkGetSkillResult](ctx, cs, "skills/get", &sdkURIParams{URI: "skill://ghost/SKILL.md"}); err == nil {
		t.Fatal("skills/get of unknown skill succeeded")
	}
	dr, err := mcp.CallCustomMethod[*sdkURIParams, *sdkDirResult](ctx, cs, "resources/directory/read", &sdkURIParams{URI: "skill://widgets"})
	if err != nil || len(dr.Resources) != 3 {
		t.Fatalf("directory read = %+v %v", dr, err)
	}

	// The SDK client sends the 2026-07-28 request metadata; the log shows it.
	reqs := s.RequestsFor("skills/get")
	if len(reqs) == 0 {
		t.Fatal("skills/get not recorded")
	}
	meta := reqs[0].Meta()
	if meta[MetaProtocolVersion] != ProtocolVersion || meta[MetaClientInfo] == nil || meta[MetaClientCapabilities] == nil {
		t.Errorf("skills/get _meta = %v", meta)
	}
	if reqs[0].Header.Get("MCP-Protocol-Version") != ProtocolVersion {
		t.Errorf("headers = %v", reqs[0].Header)
	}
	if len(s.RequestsFor("server/discover")) == 0 {
		t.Error("client did not call server/discover")
	}
}
