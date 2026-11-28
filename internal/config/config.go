// Package config reads the gateway's settings from the environment.
package config

import (
	"encoding/base64"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config is the fully resolved runtime configuration.
type Config struct {
	ListenAddr    string
	DSN           string
	PublicBaseURL string // external base, no trailing slash, e.g. https://bifrost.donkeywork.dev
	WebRoot       string // directory holding the built SPA; empty disables static serving
	MasterKey     []byte // 32 bytes, AES-256-GCM key for secrets at rest

	// Front-door OIDC (Keycloak). Empty issuer puts the server in dev mode
	// where every request is attributed to DevOwner.
	OIDCIssuer       string
	OIDCWebClientID  string // SPA client id, accepted as audience for API calls
	OIDCRequiredRole string // realm or client role the caller must hold
	OIDCScope        string // scope advertised for MCP clients, e.g. mcp:gateway
	// OIDCAudiences are further aud values a token may carry to be accepted
	// (MCPGW_OIDC_AUDIENCES, comma separated), besides the web client id and
	// {public base}/mcp: for example a token another service obtained by
	// token exchange with this gateway as its audience.
	OIDCAudiences []string
	DevOwner      string

	// RefreshInterval is how often the worker runs (MCPGW_REFRESH_INTERVAL).
	// The name is from when the worker refreshed OAuth tokens. The keyring
	// refreshes them now, so this only paces the cache sweep and the audit
	// retention purge.
	RefreshInterval time.Duration
	// RefreshLeeway is no longer used (MCPGW_REFRESH_LEEWAY). It was how
	// close to its expiry the gateway refreshed a token it held itself. It
	// is still parsed, so a deployment that sets it keeps starting.
	RefreshLeeway   time.Duration
	AuditRetention  time.Duration
	UpstreamTimeout time.Duration
	ToolsCacheTTL   time.Duration // how long a server's tools/list is served from cache; 0 disables
	Version         string
	// MetaAllowIn is the request _meta keys a client may send on to a
	// server (MCPGW_META_ALLOW_IN). MetaAllowOut is the _meta keys a server
	// may send back, on catalog items and results (MCPGW_META_ALLOW_OUT).
	// Both are comma separated, and empty lets nothing through.
	MetaAllowIn, MetaAllowOut []string
	// RuleHeaders is the HTTP request headers rules may read as
	// request.headers (MCPGW_RULE_HEADERS, comma separated). Empty shows
	// rules no header. A header that carries a credential is never shown.
	RuleHeaders []string
	// LegacyProtocolVersions is the older MCP protocol versions the front
	// door accepts besides 2026-07-28 (MCPGW_LEGACY_PROTOCOL_VERSIONS, comma
	// separated, e.g. "2025-11-25,2025-06-18,2025-03-26"). Empty by default:
	// the front door is 2026-07-28 only.
	LegacyProtocolVersions []string
	// ConnectEnabled turns on the Connect API (MCPGW_CONNECT_ENABLED). Off
	// by default: nothing is mounted and nothing listens.
	ConnectEnabled bool
	// ConnectListenAddr is where the Connect API listens when it is on
	// (MCPGW_CONNECT_LISTEN_ADDR), apart from ListenAddr.
	ConnectListenAddr string
}

// FromEnv builds a Config from MCPGW_* variables, failing on anything unusable.
func FromEnv() (Config, error) {
	c := Config{
		ListenAddr:        getenv("MCPGW_LISTEN_ADDR", ":8080"),
		DSN:               os.Getenv("MCPGW_DSN"),
		PublicBaseURL:     strings.TrimRight(getenv("MCPGW_PUBLIC_BASE_URL", "http://localhost:8080"), "/"),
		WebRoot:           os.Getenv("MCPGW_WEBROOT"),
		OIDCIssuer:        os.Getenv("MCPGW_OIDC_ISSUER"),
		OIDCWebClientID:   getenv("MCPGW_OIDC_WEB_CLIENT_ID", "mcp-gateway-web"),
		OIDCRequiredRole:  getenv("MCPGW_OIDC_REQUIRED_ROLE", "mcp-gateway-operator"),
		OIDCScope:         getenv("MCPGW_OIDC_SCOPE", "mcp:gateway"),
		DevOwner:          getenv("MCPGW_DEV_OWNER", "dev"),
		ConnectListenAddr: getenv("MCPGW_CONNECT_LISTEN_ADDR", ":50051"),
		Version:           getenv("MCPGW_VERSION", "dev"),
	}
	if c.DSN == "" {
		return c, fmt.Errorf("MCPGW_DSN is required")
	}
	list := func(env string) (out []string) {
		for _, k := range strings.Split(os.Getenv(env), ",") {
			if k = strings.TrimSpace(k); k != "" {
				out = append(out, k)
			}
		}
		return out
	}
	c.MetaAllowIn, c.MetaAllowOut = list("MCPGW_META_ALLOW_IN"), list("MCPGW_META_ALLOW_OUT")
	c.RuleHeaders = list("MCPGW_RULE_HEADERS")
	c.LegacyProtocolVersions = list("MCPGW_LEGACY_PROTOCOL_VERSIONS")
	c.OIDCAudiences = list("MCPGW_OIDC_AUDIENCES")
	var err error
	if c.ConnectEnabled, err = boolEnv("MCPGW_CONNECT_ENABLED", false); err != nil {
		return c, err
	}
	if c.RefreshInterval, err = durationEnv("MCPGW_REFRESH_INTERVAL", 4*time.Hour); err != nil {
		return c, err
	}
	// Read and validated although nothing uses it: see Config.RefreshLeeway.
	if c.RefreshLeeway, err = durationEnv("MCPGW_REFRESH_LEEWAY", 10*time.Minute); err != nil {
		return c, err
	}
	if c.AuditRetention, err = durationEnv("MCPGW_AUDIT_RETENTION", 30*24*time.Hour); err != nil {
		return c, err
	}
	if c.ToolsCacheTTL, err = durationEnv("MCPGW_TOOLS_CACHE_TTL", 5*time.Minute); err != nil {
		return c, err
	}
	if c.UpstreamTimeout, err = durationEnv("MCPGW_UPSTREAM_TIMEOUT", 60*time.Second); err != nil {
		return c, err
	}
	raw := os.Getenv("MCPGW_MASTER_KEY")
	if raw == "" {
		return c, fmt.Errorf("MCPGW_MASTER_KEY is required (base64 of 32 random bytes)")
	}
	key, err := base64.StdEncoding.DecodeString(raw)
	if err != nil || len(key) != 32 {
		return c, fmt.Errorf("MCPGW_MASTER_KEY must be base64 of exactly 32 bytes")
	}
	c.MasterKey = key
	return c, nil
}

// DevMode reports whether front-door auth is disabled.
func (c Config) DevMode() bool { return c.OIDCIssuer == "" }

func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func boolEnv(k string, def bool) (bool, error) {
	v := os.Getenv(k)
	if v == "" {
		return def, nil
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return false, fmt.Errorf("%s: want true or false, got %q", k, v)
	}
	return b, nil
}

func durationEnv(k string, def time.Duration) (time.Duration, error) {
	v := os.Getenv(k)
	if v == "" {
		return def, nil
	}
	if secs, err := strconv.Atoi(v); err == nil {
		return time.Duration(secs) * time.Second, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", k, err)
	}
	return d, nil
}
