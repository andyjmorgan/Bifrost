// Package skills is an MCP test server implementing the Skills extension
// (io.modelcontextprotocol/skills) over MCP protocol revision 2026-07-28 and
// stateless Streamable HTTP. It exists for Bifrost's e2e suite: a test mounts
// [New] on httptest.NewServer and points the gateway at it.
//
// The server is a raw JSON-RPC handler rather than a go-sdk server so that
// every byte on the wire (resultType, ttlMs, cacheScope, error codes, HTTP
// status codes) is under the test's control.
package skills

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"time"
)

// Protocol constants.
const (
	ProtocolVersion   = "2026-07-28"
	ExtensionID       = "io.modelcontextprotocol/skills"
	DirectoryMIMEType = "inode/directory"

	MetaProtocolVersion    = "io.modelcontextprotocol/protocolVersion"
	MetaClientInfo         = "io.modelcontextprotocol/clientInfo"
	MetaClientCapabilities = "io.modelcontextprotocol/clientCapabilities"
	MetaServerInfo         = "io.modelcontextprotocol/serverInfo"
)

// SupportedVersions lists the protocol versions the server accepts, newest
// first. Versions before 2026-07-28 are reachable through initialize only.
var SupportedVersions = []string{"2026-07-28", "2025-11-25", "2025-06-18", "2025-03-26"}

// JSON-RPC and MCP error codes used by the server.
const (
	CodeParseError                 = -32700
	CodeInvalidRequest             = -32600
	CodeMethodNotFound             = -32601
	CodeInvalidParams              = -32602
	CodeInternalError              = -32603
	CodeHeaderMismatch             = -32020
	CodeUnsupportedProtocolVersion = -32022
)

// SkillResource is one file of a skill with its digest and size.
type SkillResource struct {
	URI    string `json:"uri"`
	Digest string `json:"digest"`
	Size   int    `json:"size"`
}

// Skill is a skills/list or skills/get entry. Resources is either
// []SkillResource or the string "dynamic"; use [Skill.Files] and
// [Skill.IsDynamic] to inspect it.
type Skill struct {
	URI         string          `json:"uri"`
	Frontmatter map[string]any  `json:"frontmatter"`
	Resources   json.RawMessage `json:"resources"`
	Meta        map[string]any  `json:"_meta,omitempty"`
}

// IsDynamic reports whether the entry's resources is "dynamic".
func (s Skill) IsDynamic() bool { return string(s.Resources) == `"dynamic"` }

// Files decodes the manifest. It returns nil for a dynamic skill.
func (s Skill) Files() []SkillResource {
	var out []SkillResource
	if err := json.Unmarshal(s.Resources, &out); err != nil {
		return nil
	}
	return out
}

// Name returns frontmatter.name.
func (s Skill) Name() string { n, _ := s.Frontmatter["name"].(string); return n }

// Resource is the base protocol Resource metadata.
type Resource struct {
	URI         string         `json:"uri"`
	Name        string         `json:"name"`
	Title       string         `json:"title,omitempty"`
	Description string         `json:"description,omitempty"`
	MIMEType    string         `json:"mimeType,omitempty"`
	Size        *int           `json:"size,omitempty"`
	Meta        map[string]any `json:"_meta,omitempty"`
}

// ResourceTemplate is the base protocol ResourceTemplate.
type ResourceTemplate struct {
	URITemplate string `json:"uriTemplate"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	MIMEType    string `json:"mimeType,omitempty"`
}

// ResourceContents is a text or blob resources/read content item.
type ResourceContents struct {
	URI      string  `json:"uri"`
	MIMEType string  `json:"mimeType,omitempty"`
	Text     *string `json:"text,omitempty"`
	Blob     *string `json:"blob,omitempty"`
}

// Request is one JSON-RPC message received by the server, recorded verbatim.
type Request struct {
	Time   time.Time
	Method string
	ID     json.RawMessage // nil for notifications
	Params json.RawMessage // raw params object, nil when absent
	Header http.Header     // HTTP request headers (MCP-Protocol-Version, Mcp-Method, Mcp-Name, ...)
}

// ParamsMap decodes Params into a generic map (nil on failure).
func (r Request) ParamsMap() map[string]any {
	var m map[string]any
	_ = json.Unmarshal(r.Params, &m)
	return m
}

// Meta returns params._meta (nil when absent).
func (r Request) Meta() map[string]any {
	m, _ := r.ParamsMap()["_meta"].(map[string]any)
	return m
}

// URI returns params.uri, or "".
func (r Request) URI() string { u, _ := r.ParamsMap()["uri"].(string); return u }

// SkippedSkill reports a SKILL.md found under Options.Dir that was not served.
type SkippedSkill struct {
	Path   string // filesystem path of the SKILL.md
	URI    string // URI it would have had
	Reason string
}

// Digest returns the manifest digest of b: "sha256:" + lowercase hex.
func Digest(b []byte) string {
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}
