package keyring

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

// keyringVars is every setting LoadConfig reads. Tests clear them all first, so a variable in the
// developer's shell cannot leak in.
var keyringVars = []string{
	"DSN", "RUN_MIGRATIONS", "CRYPTO_ACTIVE_KEK_ID", "CRYPTO_KEKS", "PUBLIC_BASE_URL", "UI_BASE_URL", "LEGACY_HOSTS",
	"OIDC_AUTHORITY", "OIDC_INTERNAL_AUTHORITY", "OIDC_AUDIENCE", "OIDC_CLIENT_ID", "OIDC_SCOPES",
	"OIDC_WEB_CLIENT_ID", "OIDC_CLI_CLIENT_ID", "OIDC_WEB_SCOPES", "OIDC_CLI_SCOPES", "OIDC_REQUIRE_HTTPS",
	"TRUSTED_PROXIES", "AUDIT_CHANNEL_CAPACITY", "AUDIT_BATCH_SIZE", "AUDIT_FLUSH_MS", "AUDIT_RETENTION_DAYS",
	"AUDIT_SWEEP_HOURS", "AUDIT_RETENTION_BATCH",
}

func clearEnv(t *testing.T) {
	t.Helper()
	for _, name := range keyringVars {
		t.Setenv(envPrefix+name, "")
		t.Setenv(legacyEnvPrefix+name, "")
	}
}

// setKeks enables the keyring through the given prefix.
func setKeks(t *testing.T, prefix string) {
	t.Helper()
	t.Setenv(prefix+"CRYPTO_ACTIVE_KEK_ID", "local:v1")
	t.Setenv(prefix+"CRYPTO_KEKS", "local:v1="+testKek)
}

var testHost = HostConfig{
	DSN: "postgres://gateway", PublicBaseURL: "https://bifrost.test/",
	OIDCIssuer: "https://idp.test/realms/r", OIDCWebClientID: "gateway-web",
}

func TestLoadConfigDefaults(t *testing.T) {
	clearEnv(t)
	setKeks(t, envPrefix)

	c, err := LoadConfig(testHost)
	if err != nil {
		t.Fatal(err)
	}
	want := &Config{
		DSN: "postgres://gateway", RunMigrations: true,
		ActiveKekID: "local:v1", Keks: map[string]string{"local:v1": testKek},
		OAuthRedirectBaseURL: "https://bifrost.test/keyring", UIBaseURL: "https://bifrost.test", GatewayBaseURL: "https://bifrost.test",
		LegacyHosts:   []string{},
		OIDCAuthority: "https://idp.test/realms/r", OIDCScopes: "openid profile email",
		// With no web client of its own, the keyring's is the gateway's.
		OIDCWebClientID: "gateway-web", OIDCRequireHTTPS: true,
		TrustedProxies:  []string{"10.42.0.0/16", "10.43.0.0/16", "127.0.0.1/32", "::1/128"},
		AuditChannelCap: 8192, AuditBatchSize: 100, AuditFlushInterval: 500 * time.Millisecond,
		AuditRetentionDays: 180, AuditSweepInterval: 12 * time.Hour, AuditRetentionBatch: 5000,
	}
	if !reflect.DeepEqual(c, want) {
		t.Fatalf("got  %+v\nwant %+v", c, want)
	}
}

// TestLoadConfigFromVaultVariables is the cutover case: the gateway is handed Vault's variables as
// they are, and adds only what is new.
func TestLoadConfigFromVaultVariables(t *testing.T) {
	clearEnv(t)
	setKeks(t, legacyEnvPrefix)
	t.Setenv("VAULT_DSN", "postgres://vault")
	t.Setenv("VAULT_PUBLIC_BASE_URL", "https://vault.test")
	t.Setenv("VAULT_OIDC_AUTHORITY", "https://idp.test/realms/r/")
	t.Setenv("VAULT_OIDC_AUDIENCE", "vault-web")
	t.Setenv("VAULT_OIDC_WEB_CLIENT_ID", "vault-web")
	t.Setenv("VAULT_OIDC_CLI_CLIENT_ID", "vault-cli")
	t.Setenv("VAULT_OIDC_REQUIRE_HTTPS", "false")
	t.Setenv("VAULT_RUN_MIGRATIONS", "false")
	t.Setenv("VAULT_AUDIT_BATCH_SIZE", "25")
	t.Setenv("VAULT_TRUSTED_PROXIES", "10.0.0.0/8, 127.0.0.1/32")
	t.Setenv("MCPGW_KEYRING_LEGACY_HOSTS", " Vault.Test , 10.0.0.1:30820 , :30820,")

	c, err := LoadConfig(testHost)
	if err != nil {
		t.Fatal(err)
	}
	if c.DSN != "postgres://vault" || c.RunMigrations || c.OIDCRequireHTTPS || c.AuditBatchSize != 25 {
		t.Fatalf("vault variables not honoured: %+v", c)
	}
	if c.OAuthRedirectBaseURL != "https://vault.test" || c.UIBaseURL != "https://bifrost.test" {
		t.Fatalf("bases: redirect %q, ui %q", c.OAuthRedirectBaseURL, c.UIBaseURL)
	}
	if !reflect.DeepEqual(c.LegacyHosts, []string{"vault.test", "10.0.0.1:30820", ":30820"}) {
		t.Fatalf("legacy hosts: %v", c.LegacyHosts)
	}
	if !reflect.DeepEqual(c.TrustedProxies, []string{"10.0.0.0/8", "127.0.0.1/32"}) {
		t.Fatalf("trusted proxies: %v", c.TrustedProxies)
	}
	// Vault keeps its web client, and the gateway's is accepted beside it (same issuer, give or
	// take a trailing slash).
	if c.OIDCWebClientID != "vault-web" || c.OIDCCliClientID != "vault-cli" || !reflect.DeepEqual(c.ExtraWebClientIDs, []string{"gateway-web"}) {
		t.Fatalf("clients: web %q cli %q extra %v", c.OIDCWebClientID, c.OIDCCliClientID, c.ExtraWebClientIDs)
	}
}

func TestLoadConfigGatewayNameWins(t *testing.T) {
	clearEnv(t)
	setKeks(t, legacyEnvPrefix)
	t.Setenv("VAULT_DSN", "postgres://vault")
	t.Setenv("MCPGW_KEYRING_DSN", "postgres://keyring")
	t.Setenv("VAULT_PUBLIC_BASE_URL", "https://vault.test")
	t.Setenv("MCPGW_KEYRING_PUBLIC_BASE_URL", "https://callbacks.test/")
	t.Setenv("MCPGW_KEYRING_UI_BASE_URL", "https://ui.test/")
	t.Setenv("MCPGW_KEYRING_CRYPTO_ACTIVE_KEK_ID", "local:v2")
	t.Setenv("MCPGW_KEYRING_CRYPTO_KEKS", "local:v1="+testKek+", local:v2="+testKek+" ,junk")

	c, err := LoadConfig(testHost)
	if err != nil {
		t.Fatal(err)
	}
	if c.DSN != "postgres://keyring" || c.OAuthRedirectBaseURL != "https://callbacks.test" || c.UIBaseURL != "https://ui.test" {
		t.Fatalf("got dsn %q redirect %q ui %q", c.DSN, c.OAuthRedirectBaseURL, c.UIBaseURL)
	}
	// Base64 padding survives the id=key split.
	if c.ActiveKekID != "local:v2" || len(c.Keks) != 2 || c.Keks["local:v2"] != testKek {
		t.Fatalf("keks: active %q, %d keys", c.ActiveKekID, len(c.Keks))
	}
}

func TestLoadConfigGatewayClientTrust(t *testing.T) {
	cases := []struct {
		name      string
		env       map[string]string
		host      HostConfig
		wantWeb   string
		wantExtra []string
	}{
		{"same issuer, keyring has its own client", map[string]string{"OIDC_WEB_CLIENT_ID": "vault-web"}, testHost, "vault-web", []string{"gateway-web"}},
		{"same client on both sides", map[string]string{"OIDC_WEB_CLIENT_ID": "gateway-web"}, testHost, "gateway-web", nil},
		{"legacy client id setting counts as the keyring's client", map[string]string{"OIDC_CLIENT_ID": "vault-web"}, testHost, "", []string{"gateway-web"}},
		// A different issuer could hold an unrelated client of the same name: no trust.
		{"another issuer", map[string]string{"OIDC_AUTHORITY": "https://other.test", "OIDC_WEB_CLIENT_ID": "vault-web"}, testHost, "vault-web", nil},
		{"another issuer, no keyring client", map[string]string{"OIDC_AUTHORITY": "https://other.test"}, testHost, "", nil},
		{"gateway in dev mode", nil, HostConfig{DSN: "postgres://gateway", PublicBaseURL: "http://localhost:8080", OIDCWebClientID: "gateway-web"}, "", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			clearEnv(t)
			setKeks(t, envPrefix)
			for k, v := range c.env {
				t.Setenv(envPrefix+k, v)
			}
			cfg, err := LoadConfig(c.host)
			if err != nil {
				t.Fatal(err)
			}
			if cfg.OIDCWebClientID != c.wantWeb || !reflect.DeepEqual(cfg.ExtraWebClientIDs, c.wantExtra) {
				t.Fatalf("web %q extra %v; want %q %v", cfg.OIDCWebClientID, cfg.ExtraWebClientIDs, c.wantWeb, c.wantExtra)
			}
		})
	}
}

func TestLoadConfigErrors(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
		host HostConfig
		want string
	}{
		{"active id without keys", map[string]string{"CRYPTO_ACTIVE_KEK_ID": "local:v1"}, testHost, "must be set together"},
		{"keys without active id", map[string]string{"CRYPTO_KEKS": "local:v1=" + testKek}, testHost, "must be set together"},
		{"no usable pair", map[string]string{"CRYPTO_ACTIVE_KEK_ID": "local:v1", "CRYPTO_KEKS": "nonsense"}, testHost, "no id=base64 pair"},
		{"no dsn anywhere", map[string]string{"CRYPTO_ACTIVE_KEK_ID": "local:v1", "CRYPTO_KEKS": "local:v1=" + testKek}, HostConfig{PublicBaseURL: "https://bifrost.test"}, "DSN is required"},
		{"relative UI base", map[string]string{"CRYPTO_ACTIVE_KEK_ID": "local:v1", "CRYPTO_KEKS": "local:v1=" + testKek, "UI_BASE_URL": "/ui"}, testHost, "UI_BASE_URL"},
		{"relative redirect base", map[string]string{"CRYPTO_ACTIVE_KEK_ID": "local:v1", "CRYPTO_KEKS": "local:v1=" + testKek, "PUBLIC_BASE_URL": "vault.test"}, testHost, "PUBLIC_BASE_URL"},
		{"UI on a legacy host", map[string]string{"CRYPTO_ACTIVE_KEK_ID": "local:v1", "CRYPTO_KEKS": "local:v1=" + testKek, "LEGACY_HOSTS": "bifrost.test"}, testHost, "redirect to themselves"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			clearEnv(t)
			for k, v := range c.env {
				t.Setenv(envPrefix+k, v)
			}
			cfg, err := LoadConfig(c.host)
			if err == nil || cfg != nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("got %v, %v; want an error containing %q", cfg, err, c.want)
			}
			// A configuration error names variables, never what they hold.
			if strings.Contains(err.Error(), testKek) {
				t.Fatalf("error leaks key material: %v", err)
			}
		})
	}
}

func TestEnvFallbacks(t *testing.T) {
	clearEnv(t)
	t.Setenv("MCPGW_KEYRING_AUDIT_BATCH_SIZE", "notnum")
	t.Setenv("MCPGW_KEYRING_RUN_MIGRATIONS", "maybe")
	if envInt("AUDIT_BATCH_SIZE", 7) != 7 || !envBool("RUN_MIGRATIONS", true) {
		t.Fatal("an unparsable value must fall back to the default")
	}
	if got := splitList(" a, ,b,"); !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Fatalf("splitList: %v", got)
	}
	if matchHost(nil, "vault.test") || !matchHost([]string{"vault.test"}, "Vault.Test:443") {
		t.Fatal("matchHost")
	}
}
