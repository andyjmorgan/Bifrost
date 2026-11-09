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
	PublicBaseURL string // external base, no trailing slash, e.g. https://mcp-bifrost.donkeywork.dev
	WebRoot       string // directory holding the built SPA; empty disables static serving
	MasterKey     []byte // 32 bytes, AES-256-GCM key for secrets at rest

	// Front-door OIDC (Keycloak). Empty issuer puts the server in dev mode
	// where every request is attributed to DevOwner.
	OIDCIssuer       string
	OIDCWebClientID  string // SPA client id, accepted as audience for API calls
	OIDCRequiredRole string // realm or client role the caller must hold
	OIDCScope        string // scope advertised for MCP clients, e.g. mcp:gateway
	OwnerID          string // single-tenant: every role-gated human maps to this owner
	DevOwner         string

	RefreshInterval time.Duration // background token refresh cadence
	RefreshLeeway   time.Duration // refresh tokens expiring within this window
	AuditRetention  time.Duration
	UpstreamTimeout time.Duration
	ToolsCacheTTL   time.Duration // how long a server's tools/list is served from cache; 0 disables
	Version         string
}

// FromEnv builds a Config from MCPGW_* variables, failing on anything unusable.
func FromEnv() (Config, error) {
	c := Config{
		ListenAddr:       getenv("MCPGW_LISTEN_ADDR", ":8080"),
		DSN:              os.Getenv("MCPGW_DSN"),
		PublicBaseURL:    strings.TrimRight(getenv("MCPGW_PUBLIC_BASE_URL", "http://localhost:8080"), "/"),
		WebRoot:          os.Getenv("MCPGW_WEBROOT"),
		OIDCIssuer:       os.Getenv("MCPGW_OIDC_ISSUER"),
		OIDCWebClientID:  getenv("MCPGW_OIDC_WEB_CLIENT_ID", "mcp-gateway-web"),
		OIDCRequiredRole: getenv("MCPGW_OIDC_REQUIRED_ROLE", "mcp-gateway-operator"),
		OIDCScope:        getenv("MCPGW_OIDC_SCOPE", "mcp:gateway"),
		OwnerID:          getenv("MCPGW_OWNER_ID", "owner"),
		DevOwner:         getenv("MCPGW_DEV_OWNER", "dev"),
		Version:          getenv("MCPGW_VERSION", "dev"),
	}
	if c.DSN == "" {
		return c, fmt.Errorf("MCPGW_DSN is required")
	}
	var err error
	if c.RefreshInterval, err = durationEnv("MCPGW_REFRESH_INTERVAL", 4*time.Hour); err != nil {
		return c, err
	}
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
