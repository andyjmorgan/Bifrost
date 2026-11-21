package gateway_test

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/andyjmorgan/Bifrost/internal/e2e/upstream/skills"
	"github.com/andyjmorgan/Bifrost/internal/store"
	"github.com/andyjmorgan/Bifrost/pkg/hooks"
	"github.com/andyjmorgan/Bifrost/pkg/hooks/harness"
)

// Skills over the real front door: on the direct route the gateway
// advertises the skills extension when the server does, and serves
// skills/list and skills/get through skills/list.post. The mux route does
// neither.

func serveSkills(t *testing.T, r *rig, slug string, rules ...hooks.Rule) *skills.Server {
	t.Helper()
	up, err := skills.New(skills.Options{})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(up)
	t.Cleanup(ts.Close)
	s := &store.Server{ID: uuid.New(), Name: "Skills", URL: ts.URL, Enabled: true, Slug: slug, Hooks: stored(t, rules...), Generation: 1, UpdatedAt: time.Now()}
	r.store.mu.Lock()
	r.store.servers = append(r.store.servers, s)
	r.store.mu.Unlock()
	return up
}

func skillURIs(t *testing.T, rep *harness.RawReply) []string {
	t.Helper()
	res := rep.Result()
	if res == nil {
		t.Fatalf("no result: %v", rep.Final())
	}
	var out []string
	list, _ := res["skills"].([]any)
	for _, s := range list {
		m, _ := s.(map[string]any)
		uri, _ := m["uri"].(string)
		out = append(out, uri)
	}
	return out
}

func TestSkillsAreServedOnTheDirectRouteThroughTheRules(t *testing.T) {
	r := newRig(t)
	serveSkills(t, r, "s", deny("skills/list.post", "skill.uri.startsWith('skill://widgets/')"))

	disc := harness.Raw(t, r.direct("s"), harness.RawRequest{Method: "server/discover"}).Result()
	if !strings.Contains(dump(disc), hooks.ExtSkills) {
		t.Fatalf("the direct route does not advertise skills: %s", dump(disc))
	}
	if mux := harness.Raw(t, r.mux(), harness.RawRequest{Method: "server/discover"}).Result(); strings.Contains(dump(mux), hooks.ExtSkills) {
		t.Fatalf("the mux advertises skills: %s", dump(mux))
	}

	got := skillURIs(t, harness.Raw(t, r.direct("s"), harness.RawRequest{Method: "skills/list"}))
	joined := strings.Join(got, " ")
	if !strings.Contains(joined, skills.NotebooksSkill) {
		t.Errorf("skills/list = %v, want %s", got, skills.NotebooksSkill)
	}
	if strings.Contains(joined, "skill://widgets/") {
		t.Errorf("a denied skill, or one nested under it, was listed: %v", got)
	}

	ok := harness.Raw(t, r.direct("s"), harness.RawRequest{Method: "skills/get", Params: map[string]any{"uri": skills.NotebooksSkill}}).Result()
	if !strings.Contains(dump(ok), skills.NotebooksSkill) {
		t.Errorf("skills/get of a visible skill = %s", dump(ok))
	}
	hidden := harness.Raw(t, r.direct("s"), harness.RawRequest{Method: "skills/get", Params: map[string]any{"uri": skills.ChartsSkill}})
	if hidden.Result() != nil {
		t.Errorf("skills/get of a hidden skill answered: %v", hidden.Final())
	}

	read := harness.Raw(t, r.direct("s"), harness.RawRequest{Method: "resources/read", Params: map[string]any{"uri": skills.NotebooksSkill}}).Result()
	if !strings.Contains(dump(read), "contents") {
		t.Errorf("reading a skill's SKILL.md = %s", dump(read))
	}

	if rep := harness.Raw(t, r.mux(), harness.RawRequest{Method: "skills/list"}); rep.Result() != nil {
		t.Errorf("the mux answered skills/list: %v", rep.Final())
	}
}
