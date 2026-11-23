package keyring

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// Every keyring setting is read as MCPGW_KEYRING_<NAME> first and VAULT_<NAME> second, so the
// variables a Vault deployment already carries can be handed to the gateway unchanged.
const (
	envPrefix       = "MCPGW_KEYRING_"
	legacyEnvPrefix = "VAULT_"
)

// MountPrefix is where the keyring's API lives on every host the gateway answers on.
const MountPrefix = "/keyring"

// defaultTrustedProxies are the k3s pod and service ranges plus loopback: the peers whose
// X-Forwarded-For is believed when resolving a caller's address for the audit trail.
const defaultTrustedProxies = "10.42.0.0/16,10.43.0.0/16,127.0.0.1/32,::1/128"

// HostConfig is what the keyring inherits from the gateway's own configuration when it has no
// setting of its own.
type HostConfig struct {
	DSN             string // the gateway's database, used unless the keyring names another
	PublicBaseURL   string // the gateway's external base, no trailing slash
	OIDCIssuer      string // empty when the gateway runs in dev mode
	OIDCWebClientID string // the client the gateway's SPA signs in with
}

// Config is the fully resolved keyring configuration.
type Config struct {
	DSN           string
	RunMigrations bool

	ActiveKekID string
	Keks        map[string]string

	// OAuthRedirectBaseURL is the base of the redirect URI sent to OAuth providers
	// ({base}/api/oauth/callback). It must be the address registered with the provider apps, so it
	// keeps pointing at the old Vault host for as long as those registrations do.
	OAuthRedirectBaseURL string
	// UIBaseURL is where browsers are sent: after an OAuth callback, and when a page is requested
	// on a legacy host. It is independent of OAuthRedirectBaseURL.
	UIBaseURL string
	// LegacyHosts are the request hosts on which the keyring is served at Vault's original paths.
	// An entry is a host name, which matches any port; host:port, which matches only that port; or
	// :port, which matches any host on that port (a NodePort, reached by whichever node address).
	LegacyHosts []string

	OIDCAuthority    string
	OIDCInternal     string
	OIDCAudience     string
	OIDCClientID     string
	OIDCScopes       string
	OIDCWebClientID  string
	OIDCCliClientID  string
	OIDCWebScopes    string
	OIDCCliScopes    string
	OIDCRequireHTTPS bool
	// ExtraWebClientIDs are OAuth clients accepted as web callers besides the keyring's own: the
	// gateway's SPA client, when both verify against the same issuer.
	ExtraWebClientIDs []string

	TrustedProxies      []string
	AuditChannelCap     int
	AuditBatchSize      int
	AuditFlushInterval  time.Duration
	AuditRetentionDays  int
	AuditSweepInterval  time.Duration
	AuditRetentionBatch int
}

// LoadConfig reads the keyring configuration from the environment. The keyring is off unless its
// KEKs are configured: with neither KEK variable set it returns (nil, nil) and the gateway runs
// exactly as it does without the module. A half-configured keyring is an error, not a silent off.
func LoadConfig(host HostConfig) (*Config, error) {
	activeKek, rawKeks := env("CRYPTO_ACTIVE_KEK_ID"), env("CRYPTO_KEKS")
	if activeKek == "" && rawKeks == "" {
		return nil, nil
	}
	if activeKek == "" || rawKeks == "" {
		return nil, errors.New("keyring: " + envPrefix + "CRYPTO_ACTIVE_KEK_ID and " + envPrefix + "CRYPTO_KEKS must be set together")
	}
	keks := parseKeks(rawKeks)
	if len(keks) == 0 {
		return nil, errors.New("keyring: " + envPrefix + "CRYPTO_KEKS holds no id=base64 pair")
	}

	hostBase := strings.TrimRight(host.PublicBaseURL, "/")
	c := &Config{
		DSN:                  firstNonEmpty(env("DSN"), host.DSN),
		RunMigrations:        envBool("RUN_MIGRATIONS", true),
		ActiveKekID:          activeKek,
		Keks:                 keks,
		OAuthRedirectBaseURL: strings.TrimRight(firstNonEmpty(env("PUBLIC_BASE_URL"), hostBase+MountPrefix), "/"),
		UIBaseURL:            strings.TrimRight(firstNonEmpty(env("UI_BASE_URL"), hostBase), "/"),
		LegacyHosts:          normalizeHosts(splitList(env("LEGACY_HOSTS"))),
		OIDCAuthority:        firstNonEmpty(env("OIDC_AUTHORITY"), host.OIDCIssuer),
		OIDCInternal:         env("OIDC_INTERNAL_AUTHORITY"),
		OIDCAudience:         env("OIDC_AUDIENCE"),
		OIDCClientID:         env("OIDC_CLIENT_ID"),
		OIDCScopes:           firstNonEmpty(env("OIDC_SCOPES"), "openid profile email"),
		OIDCWebClientID:      env("OIDC_WEB_CLIENT_ID"),
		OIDCCliClientID:      env("OIDC_CLI_CLIENT_ID"),
		OIDCWebScopes:        env("OIDC_WEB_SCOPES"),
		OIDCCliScopes:        env("OIDC_CLI_SCOPES"),
		OIDCRequireHTTPS:     envBool("OIDC_REQUIRE_HTTPS", true),
		TrustedProxies:       splitList(firstNonEmpty(env("TRUSTED_PROXIES"), defaultTrustedProxies)),
		AuditChannelCap:      envInt("AUDIT_CHANNEL_CAPACITY", 8192),
		AuditBatchSize:       envInt("AUDIT_BATCH_SIZE", 100),
		AuditFlushInterval:   time.Duration(envInt("AUDIT_FLUSH_MS", 500)) * time.Millisecond,
		AuditRetentionDays:   envInt("AUDIT_RETENTION_DAYS", 180),
		AuditSweepInterval:   time.Duration(envInt("AUDIT_SWEEP_HOURS", 12)) * time.Hour,
		AuditRetentionBatch:  envInt("AUDIT_RETENTION_BATCH", 5000),
	}
	if c.DSN == "" {
		return nil, errors.New("keyring: a database DSN is required (" + envPrefix + "DSN or MCPGW_DSN)")
	}

	// The gateway's SPA signs in with the gateway's own client. Its tokens are only verifiable when
	// both sides use the same issuer, so that is the only case in which the client is trusted.
	if host.OIDCWebClientID != "" && sameIssuer(c.OIDCAuthority, host.OIDCIssuer) {
		if c.OIDCWebClientID == "" && c.OIDCClientID == "" && c.OIDCAudience == "" {
			c.OIDCWebClientID = host.OIDCWebClientID
		} else if host.OIDCWebClientID != firstNonEmpty(c.OIDCWebClientID, c.OIDCClientID, c.OIDCAudience) {
			c.ExtraWebClientIDs = []string{host.OIDCWebClientID}
		}
	}

	ui, err := url.Parse(c.UIBaseURL)
	if err != nil || ui.Host == "" {
		return nil, errors.New("keyring: " + envPrefix + "UI_BASE_URL must be an absolute URL")
	}
	if matchHost(c.LegacyHosts, ui.Host) {
		return nil, fmt.Errorf("keyring: the UI base host %q is listed in %sLEGACY_HOSTS; pages there would redirect to themselves", ui.Host, envPrefix)
	}
	if redirect, err := url.Parse(c.OAuthRedirectBaseURL); err != nil || redirect.Host == "" {
		return nil, errors.New("keyring: " + envPrefix + "PUBLIC_BASE_URL must be an absolute URL")
	}
	return c, nil
}

// env reads a keyring setting: the gateway-prefixed name first, then Vault's.
func env(name string) string {
	if v := os.Getenv(envPrefix + name); v != "" {
		return v
	}
	return os.Getenv(legacyEnvPrefix + name)
}

func envInt(name string, def int) int {
	if n, err := strconv.Atoi(env(name)); err == nil {
		return n
	}
	return def
}

func envBool(name string, def bool) bool {
	if b, err := strconv.ParseBool(env(name)); err == nil {
		return b
	}
	return def
}

// parseKeks parses "id1=base64,id2=base64" into a map. A base64 value may end in '=' padding, so
// only the first '=' of a pair separates the id from the key.
func parseKeks(raw string) map[string]string {
	out := map[string]string{}
	for _, pair := range strings.Split(raw, ",") {
		pair = strings.TrimSpace(pair)
		if eq := strings.IndexByte(pair, '='); eq > 0 {
			out[strings.TrimSpace(pair[:eq])] = strings.TrimSpace(pair[eq+1:])
		}
	}
	return out
}

func splitList(raw string) []string {
	var out []string
	for _, p := range strings.Split(raw, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func sameIssuer(a, b string) bool {
	return a != "" && strings.TrimRight(a, "/") == strings.TrimRight(b, "/")
}

func normalizeHosts(hosts []string) []string {
	out := make([]string, 0, len(hosts))
	for _, h := range hosts {
		out = append(out, strings.ToLower(h))
	}
	return out
}

// matchHost reports whether a request host is one of the configured hosts. An entry of host:port
// must match the request's host and port, a bare host matches on any port, and a bare :port matches
// any host on that port.
func matchHost(hosts []string, requestHost string) bool {
	if len(hosts) == 0 {
		return false
	}
	full := strings.ToLower(requestHost)
	name, port := full, ""
	if h, p, err := net.SplitHostPort(full); err == nil {
		name, port = h, ":"+p
	}
	for _, h := range hosts {
		if h == full || h == name || (port != "" && h == port) {
			return true
		}
	}
	return false
}
