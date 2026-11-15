package skills

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"path"
	"slices"
	"strings"
	"unicode/utf8"

	"go.yaml.in/yaml/v3"
)

type file struct {
	content []byte
	digest  string
	mime    string
}

func newFile(uri string, content []byte) *file {
	c := bytes.Clone(content)
	return &file{content: c, digest: Digest(c), mime: MIMEType(uri, c)}
}

// MIMEType guesses a mimeType from the URI's extension, falling back to
// content sniffing (text/plain for UTF-8, application/octet-stream otherwise).
func MIMEType(uri string, content []byte) string {
	ext := strings.ToLower(path.Ext(uri))
	switch ext {
	case ".md":
		return "text/markdown"
	case ".txt":
		return "text/plain"
	case ".py":
		return "text/x-python"
	case ".json":
		return "application/json"
	case ".xml", ".xsd":
		return "application/xml"
	case ".html":
		return "text/html"
	case ".js", ".mjs":
		return "text/javascript"
	case ".sh":
		return "application/x-sh"
	case ".ttf":
		return "font/ttf"
	case ".pdf":
		return "application/pdf"
	case ".gz":
		return "application/gzip"
	case ".png":
		return "image/png"
	}
	if t := mime.TypeByExtension(ext); t != "" {
		if mt, _, err := mime.ParseMediaType(t); err == nil {
			return mt
		}
	}
	if utf8.Valid(content) && !bytes.ContainsRune(content, 0) {
		return "text/plain"
	}
	return "application/octet-stream"
}

// IsText reports whether content is served as `text` (rather than `blob`).
// Text is served only when it is valid UTF-8 without NUL bytes and the
// mimeType is textual, so JSON transport round-trips the exact bytes.
func IsText(mimeType string, content []byte) bool {
	if !utf8.Valid(content) || bytes.ContainsRune(content, 0) {
		return false
	}
	switch {
	case strings.HasPrefix(mimeType, "text/"),
		mimeType == "application/json",
		mimeType == "application/xml",
		mimeType == "application/x-sh":
		return true
	}
	return false
}

// contents builds a resources/read content item for raw bytes.
func contents(uri, mimeType string, b []byte) ResourceContents {
	rc := ResourceContents{URI: uri, MIMEType: mimeType}
	if IsText(mimeType, b) {
		s := string(b)
		rc.Text = &s
	} else {
		s := base64.StdEncoding.EncodeToString(b)
		rc.Blob = &s
	}
	return rc
}

// ParseFrontmatter extracts and decodes the YAML frontmatter of a SKILL.md.
// The result is normalised through JSON so it compares equal to what a client
// decodes from the wire.
func ParseFrontmatter(b []byte) (map[string]any, error) {
	s := string(b)
	s = strings.TrimPrefix(s, "\ufeff")
	nl := "\n"
	switch {
	case strings.HasPrefix(s, "---\r\n"):
		nl = "\r\n"
	case strings.HasPrefix(s, "---\n"):
	default:
		return nil, errors.New("SKILL.md does not begin with a --- frontmatter fence")
	}
	rest := s[len("---"+nl):]
	var yamlText string
	if strings.HasPrefix(rest, "---"+nl) || rest == "---" {
		yamlText = ""
	} else {
		end := strings.Index(rest, nl+"---")
		if end < 0 {
			return nil, errors.New("unterminated frontmatter")
		}
		after := rest[end+len(nl+"---"):]
		if after != "" && !strings.HasPrefix(after, nl) {
			return nil, errors.New("frontmatter closing fence is not on its own line")
		}
		yamlText = rest[:end]
	}
	var raw map[string]any
	if err := yaml.Unmarshal([]byte(yamlText), &raw); err != nil {
		return nil, fmt.Errorf("frontmatter yaml: %w", err)
	}
	if raw == nil {
		raw = map[string]any{}
	}
	j, err := json.Marshal(raw)
	if err != nil {
		return nil, fmt.Errorf("frontmatter to json: %w", err)
	}
	var fm map[string]any
	if err := json.Unmarshal(j, &fm); err != nil {
		return nil, err
	}
	if n, _ := fm["name"].(string); n == "" {
		return nil, errors.New("frontmatter has no name")
	}
	if d, _ := fm["description"].(string); d == "" {
		return nil, errors.New("frontmatter has no description")
	}
	return fm, nil
}

// skillRoot returns the skill root for a SKILL.md URI.
func skillRoot(skillMD string) (string, bool) {
	root, ok := strings.CutSuffix(skillMD, "/SKILL.md")
	if !ok || root == "" {
		return "", false
	}
	// Require scheme://something.
	i := strings.Index(root, "://")
	if i <= 0 || len(root) == i+3 {
		return "", false
	}
	return root, true
}

// validateSkill checks a SKILL.md's frontmatter against its URI.
func validateSkill(skillMD string, content []byte) (map[string]any, error) {
	root, ok := skillRoot(skillMD)
	if !ok {
		return nil, fmt.Errorf("%s is not a SKILL.md URI", skillMD)
	}
	fm, err := ParseFrontmatter(content)
	if err != nil {
		return nil, err
	}
	last := root[strings.LastIndex(root, "/")+1:]
	if fm["name"] != last {
		return nil, fmt.Errorf("frontmatter name %q does not match directory %q", fm["name"], last)
	}
	return fm, nil
}

// children lists direct children of dir among uris (sorted input not
// required). ok is false when dir is not a directory (no URI beneath it).
func children(dir string, uris []string) (files []string, dirs []string, ok bool) {
	if dir == "" || strings.HasSuffix(dir, "/") {
		return nil, nil, false
	}
	if i := strings.Index(dir, "://"); i <= 0 || len(dir) == i+3 {
		return nil, nil, false
	}
	prefix := dir + "/"
	seen := map[string]bool{}
	for _, u := range uris {
		rest, found := strings.CutPrefix(u, prefix)
		if !found || rest == "" {
			continue
		}
		ok = true
		if seg, _, nested := strings.Cut(rest, "/"); nested {
			if !seen[seg] {
				seen[seg] = true
				dirs = append(dirs, prefix+seg)
			}
		} else {
			files = append(files, u)
		}
	}
	slices.Sort(files)
	slices.Sort(dirs)
	return files, dirs, ok
}
