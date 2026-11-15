package skills

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// LoadDir walks dir and returns the files of every valid skill found in it,
// keyed by URI, plus the SKILL.md files it skipped. Every directory holding a
// SKILL.md is a skill (nested ones too). A skill is skipped when its
// frontmatter does not parse or its folder name differs from frontmatter
// name. Files are served only when they sit inside at least one valid skill.
//
// URIs are skill://<prefix>/<path relative to dir>; with an empty prefix they
// are skill://<path relative to dir>. ".git" directories and symlinks are
// ignored.
func LoadDir(dir, prefix string) (map[string][]byte, []SkippedSkill, error) {
	prefix = strings.Trim(prefix, "/")
	toURI := func(rel string) string {
		rel = filepath.ToSlash(rel)
		if prefix == "" {
			return "skill://" + rel
		}
		return "skill://" + prefix + "/" + rel
	}

	var all []string // relative paths of regular files
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		all = append(all, rel)
		return nil
	})
	if err != nil {
		return nil, nil, fmt.Errorf("walk %s: %w", dir, err)
	}
	slices.Sort(all)

	var roots []string // valid skill roots, relative, slash form
	var skipped []SkippedSkill
	for _, rel := range all {
		if filepath.Base(rel) != "SKILL.md" {
			continue
		}
		relDir := filepath.ToSlash(filepath.Dir(rel))
		uri := toURI(filepath.ToSlash(rel))
		if relDir == "." && prefix == "" {
			skipped = append(skipped, SkippedSkill{Path: filepath.Join(dir, rel), URI: uri, Reason: "SKILL.md at the root of dir with no prefix has no skill directory"})
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, rel))
		if err != nil {
			return nil, nil, err
		}
		if _, err := validateSkill(uri, b); err != nil {
			skipped = append(skipped, SkippedSkill{Path: filepath.Join(dir, rel), URI: uri, Reason: err.Error()})
			continue
		}
		roots = append(roots, relDir)
	}

	files := map[string][]byte{}
	for _, rel := range all {
		r := filepath.ToSlash(rel)
		inSkill := slices.ContainsFunc(roots, func(root string) bool {
			return root == "." || strings.HasPrefix(r, root+"/")
		})
		if !inSkill {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, rel))
		if err != nil {
			return nil, nil, err
		}
		files[toURI(r)] = b
	}
	return files, skipped, nil
}
