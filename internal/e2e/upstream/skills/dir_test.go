package skills

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// realSkillsDir is a gitignored checkout of github.com/anthropics/skills.
const realSkillsDir = "../../testdata/real/anthropic-skills"

func requireRealSkills(t *testing.T) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(realSkillsDir, "skills")); err != nil {
		t.Skipf("real skills checkout absent (%s): %v", realSkillsDir, err)
	}
}

func TestLoadDirSynthetic(t *testing.T) {
	dir := t.TempDir()
	write := func(rel, body string) {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("good/SKILL.md", "---\nname: good\ndescription: ok\n---\nbody\n")
	write("good/bin/data.bin", "\x00\x01\xff")
	write("good/inner/SKILL.md", "---\nname: inner\ndescription: nested\n---\n")
	write("bad/SKILL.md", "---\nname: wrong\ndescription: mismatched\n---\n")
	write("bad/extra.md", "orphan\n")
	write("README.md", "not in a skill\n")

	s, c := newTestServer(t, Options{NoSynthetic: true, Dir: dir, DirPrefix: "local"})
	if sk := s.Skipped(); len(sk) != 1 || sk[0].URI != "skill://local/bad/SKILL.md" {
		t.Fatalf("skipped = %+v", sk)
	}
	all := c.listAllSkills(t)
	if len(all) != 2 || all[0].URI != "skill://local/good/SKILL.md" || all[1].URI != "skill://local/good/inner/SKILL.md" {
		t.Fatalf("skills = %s", mustJSON(all))
	}
	if got := len(all[0].Files()); got != 3 {
		t.Errorf("good manifest has %d files, want 3 (includes nested)", got)
	}
	for _, u := range []string{"skill://local/bad/extra.md", "skill://local/README.md"} {
		if code := c.errCode("resources/read", map[string]any{"uri": u}); code != CodeInvalidParams {
			t.Errorf("%s served (code %d)", u, code)
		}
	}
	b, mt := c.readBytes(t, "skill://local/good/bin/data.bin")
	if !bytes.Equal(b, []byte("\x00\x01\xff")) || mt != "application/octet-stream" {
		t.Errorf("binary read %q %s", b, mt)
	}
}

func TestRealSkillsDir(t *testing.T) {
	requireRealSkills(t)
	dir := filepath.Join(realSkillsDir, "skills")
	s, c := newTestServer(t, Options{NoSynthetic: true, Dir: dir})
	for _, sk := range s.Skipped() {
		t.Logf("skipped %s: %s", sk.URI, sk.Reason)
	}

	skills := c.listAllSkills(t)
	if len(skills) == 0 {
		t.Fatal("no skills loaded")
	}
	t.Logf("%d skills served, %d skipped", len(skills), len(s.Skipped()))

	var files, blobs int
	for _, sk := range skills {
		if !strings.HasPrefix(sk.URI, "skill://anthropic/") {
			t.Errorf("unexpected URI %s", sk.URI)
		}
		root := strings.TrimSuffix(sk.URI, "/SKILL.md")
		if root[strings.LastIndex(root, "/")+1:] != sk.Name() {
			t.Errorf("%s: name %q", sk.URI, sk.Name())
		}
		manifest := sk.Files()
		if manifest == nil {
			t.Fatalf("%s: resources %s", sk.URI, sk.Resources)
		}
		// Every manifest entry matches the bytes on disk.
		onDisk := 0
		_ = filepath.WalkDir(filepath.Join(dir, strings.TrimPrefix(root, "skill://anthropic/")), func(p string, d os.DirEntry, err error) error {
			if err == nil && d.Type().IsRegular() {
				onDisk++
			}
			return err
		})
		if onDisk != len(manifest) {
			t.Errorf("%s: manifest %d files, disk %d", sk.URI, len(manifest), onDisk)
		}
		for _, f := range manifest {
			p := filepath.Join(dir, filepath.FromSlash(strings.TrimPrefix(f.URI, "skill://anthropic/")))
			b, err := os.ReadFile(p)
			if err != nil {
				t.Errorf("%s: %v", f.URI, err)
				continue
			}
			if f.Size != len(b) || f.Digest != digestByHand(b) {
				t.Errorf("%s: manifest %s/%d, disk %s/%d", f.URI, f.Digest, f.Size, digestByHand(b), len(b))
			}
			files++
		}
		// Frontmatter matches SKILL.md on disk.
		md, _ := os.ReadFile(filepath.Join(dir, strings.TrimPrefix(sk.URI, "skill://anthropic/")))
		fm, err := ParseFrontmatter(md)
		if err != nil || mustJSON(fm) != mustJSON(sk.Frontmatter) {
			t.Errorf("%s: frontmatter mismatch (%v)", sk.URI, err)
		}
	}

	// Binary files come back as blobs with exact bytes and a real mimeType.
	for _, u := range s.URIs() {
		ext := strings.ToLower(filepath.Ext(u))
		if ext != ".ttf" && ext != ".pdf" && ext != ".gz" {
			continue
		}
		b, mt := c.readBytes(t, u)
		want, _ := s.File(u)
		if !bytes.Equal(b, want) {
			t.Errorf("%s: bytes differ", u)
		}
		if mt == "" || strings.HasPrefix(mt, "text/") {
			t.Errorf("%s: mimeType %q", u, mt)
		}
		blobs++
		if blobs >= 5 {
			break
		}
	}
	t.Logf("verified %d manifest files against disk, %d binary reads", files, blobs)
}

func TestRealSkillsRootSkipsMismatchedName(t *testing.T) {
	requireRealSkills(t)
	s := MustNew(Options{NoSynthetic: true, Dir: realSkillsDir})
	var found bool
	for _, sk := range s.Skipped() {
		if sk.URI == "skill://anthropic/template/SKILL.md" && strings.Contains(sk.Reason, "template-skill") {
			found = true
		}
	}
	if !found {
		t.Errorf("template skill not reported as skipped: %+v", s.Skipped())
	}
	if _, err := s.GetSkill("skill://anthropic/skills/pdf/SKILL.md"); err != nil {
		t.Errorf("pdf under root layout: %v", err)
	}
}
