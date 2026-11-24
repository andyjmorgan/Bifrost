// Package keyring embeds the DonkeyWork Vault server in the gateway: stored secrets, OAuth accounts
// and their providers, access keys and an audit trail, on the Postgres schema `vault` with the DWV1
// envelope format, both unchanged from Vault.
//
// The keyring keeps Vault's own HTTP surface and authentication. This package decides only where
// that surface is reachable: under /keyring on every host, and at Vault's original paths on the
// hosts that used to be Vault's. It is off unless its KEKs are configured.
package keyring

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/andyjmorgan/Bifrost/internal/keyring/audit"
	"github.com/andyjmorgan/Bifrost/internal/keyring/crypto"
	"github.com/andyjmorgan/Bifrost/internal/keyring/db"
	"github.com/andyjmorgan/Bifrost/internal/keyring/httpapi"
	"github.com/andyjmorgan/Bifrost/internal/keyring/httpx"
	"github.com/andyjmorgan/Bifrost/internal/keyring/manifests"
	"github.com/andyjmorgan/Bifrost/internal/keyring/service"
	"github.com/andyjmorgan/Bifrost/internal/keyring/store"
	"github.com/andyjmorgan/Bifrost/internal/keyring/telemetry"
	"github.com/andyjmorgan/Bifrost/internal/oauthclient"
)

// UI routes of the gateway's SPA that the keyring sends browsers to.
const (
	uiLandingPath = "/secrets"
	uiConnectPath = "/connect"
)

// Keyring is the running module: the Vault API handler, its audit workers and its database pool.
// A nil *Keyring is the disabled module; Wrap, Start and Shutdown are safe to call on it.
type Keyring struct {
	cfg *Config
	log *slog.Logger
	api http.Handler

	source    *Source
	auditLog  *audit.Log
	writer    *audit.Writer
	retention *audit.Retention
	closeDB   func()

	workers sync.WaitGroup
	cancel  context.CancelFunc
}

// FromEnv builds the keyring from the environment. It returns (nil, nil) when the keyring is not
// configured, which is the default.
func FromEnv(ctx context.Context, host HostConfig, log *slog.Logger) (*Keyring, error) {
	cfg, err := LoadConfig(host)
	if err != nil || cfg == nil {
		return nil, err
	}
	return Open(ctx, cfg, log)
}

// Open connects to the keyring's database, applies its migrations and builds the module. The
// migrations are the keyring's own (schema `vault`, recorded in vault.schema_migrations) and never
// touch the gateway's numbered ones.
func Open(ctx context.Context, cfg *Config, log *slog.Logger) (*Keyring, error) {
	pg, err := store.NewPostgres(ctx, cfg.DSN)
	if err != nil {
		return nil, err
	}
	if cfg.RunMigrations {
		if err := db.Migrate(ctx, pg.Pool()); err != nil {
			pg.Close()
			return nil, err
		}
	}
	k, err := build(ctx, cfg, pg, log)
	if err != nil {
		pg.Close()
		return nil, err
	}
	k.closeDB = pg.Close
	return k, nil
}

// New builds the module over a store the caller already has, without connecting to a database or
// applying migrations. It is how a test runs the whole keyring on the in-memory store; a server
// calls Open.
func New(ctx context.Context, cfg *Config, st store.Store, log *slog.Logger) (*Keyring, error) {
	return build(ctx, cfg, st, log)
}

// build wires the services over any Store, so tests can run the whole module on the in-memory one.
func build(ctx context.Context, cfg *Config, st store.Store, log *slog.Logger) (*Keyring, error) {
	if log == nil {
		log = slog.Default()
	}
	metrics, err := telemetry.NewMetrics()
	if err != nil {
		return nil, err
	}
	kek, err := crypto.NewLocalKekProvider(cfg.ActiveKekID, cfg.Keks)
	if err != nil {
		return nil, err
	}
	cipher := crypto.NewEnvelopeCipher(kek)

	loader, err := manifests.NewLoader()
	if err != nil {
		return nil, err
	}
	resolver := manifests.NewResolver(st, loader)

	auditLog := audit.NewLog(cfg.AuditChannelCap, log, metrics)

	// Provider endpoints are partly user-supplied, so outbound calls go through the transport that
	// refuses link-local and metadata destinations.
	oauthClient := &http.Client{Timeout: 30 * time.Second, Transport: httpx.DefaultSafeTransport()}

	// The gateway's own OAuth client, for the connections of MCP servers. It is built from the
	// gateway's public base and nothing else, so its client metadata URL and its callback are the
	// gateway's: the identity every MCP authorization server already knows it by.
	serverOAuth := oauthclient.New(cfg.GatewayBaseURL, &http.Client{Timeout: 20 * time.Second, Transport: httpx.DefaultSafeTransport()})

	secrets := service.NewAPIKeyService(st, cipher, auditLog)
	tokens := service.NewOAuthTokenService(st, cipher, auditLog, resolver, oauthClient,
		service.WithServerOAuth(serverOAuth), service.WithRefreshGuard(cfg.RefreshGuard))
	flow := service.NewOAuthFlowService(st, cipher, resolver, auditLog, oauthClient, log, service.WithServerFlow(serverOAuth))
	providers := service.NewProviderService(st, resolver, auditLog)

	srv, err := httpapi.NewServer(ctx, httpapi.Deps{
		APIKeys:      secrets,
		AccessKeys:   service.NewAccessKeyService(st, auditLog),
		OAuthConfigs: service.NewOAuthConfigService(st, cipher, auditLog, resolver),
		OAuthTokens:  tokens,
		OAuthFlow:    flow,
		Providers:    providers,
		Resolver:     resolver,
		Discovery:    manifests.NewDiscovery(oauthClient),
		AuditLog:     auditLog,
		AuditQuery:   audit.NewQueryService(st, auditLog),
		IPResolver:   audit.NewForwardedIPResolver(cfg.TrustedProxies),
		Logger:       log,
		OIDC: httpapi.OIDCConfig{
			Authority: cfg.OIDCAuthority, InternalAuthority: cfg.OIDCInternal, Audience: cfg.OIDCAudience,
			ClientID: cfg.OIDCClientID, Scopes: cfg.OIDCScopes,
			WebClientID: cfg.OIDCWebClientID, CliClientID: cfg.OIDCCliClientID,
			WebScopes: cfg.OIDCWebScopes, CliScopes: cfg.OIDCCliScopes,
			RequireHTTPS: cfg.OIDCRequireHTTPS, ExtraWebClientIDs: cfg.ExtraWebClientIDs,
		},
		PublicBaseURL:   cfg.OAuthRedirectBaseURL,
		PostCallbackURL: cfg.UIBaseURL + uiConnectPath,
	})
	if err != nil {
		return nil, err
	}

	return &Keyring{
		cfg: cfg, log: log, api: srv.Handler(),
		source: &Source{store: st, resolver: resolver, cipher: cipher, secrets: secrets, tokens: tokens, flow: flow,
			providers: providers, audit: auditLog, redirectURI: serverOAuth.RedirectURI},
		auditLog: auditLog,
		writer: audit.NewWriter(auditLog, st, log, metrics, audit.WriterOptions{
			BatchSize: cfg.AuditBatchSize, FlushInterval: cfg.AuditFlushInterval}),
		retention: audit.NewRetention(st, log, audit.RetentionOptions{
			RetentionDays: cfg.AuditRetentionDays, SweepInterval: cfg.AuditSweepInterval, BatchSize: cfg.AuditRetentionBatch}),
	}, nil
}

// LegacyHosts returns the hosts on which the keyring answers at Vault's original paths.
func (k *Keyring) LegacyHosts() []string {
	if k == nil {
		return nil
	}
	return k.cfg.LegacyHosts
}

// Start launches the audit writer and the retention sweeper. They run until Shutdown.
func (k *Keyring) Start() {
	if k == nil || k.cancel != nil {
		return
	}
	// Not derived from the process context: on a signal the writer must outlive the HTTP server
	// long enough to flush what the last requests queued. Shutdown ends it.
	ctx, cancel := context.WithCancel(context.Background())
	k.cancel = cancel
	k.workers.Add(2)
	go func() { defer k.workers.Done(); k.writer.Run(ctx) }()
	go func() { defer k.workers.Done(); k.retention.Run(ctx) }()
}

// Shutdown flushes the audit trail, stops the workers and closes the database pool. Call it once
// the HTTP server has stopped taking requests.
func (k *Keyring) Shutdown() {
	if k == nil {
		return
	}
	k.auditLog.Complete()
	if k.cancel != nil {
		k.cancel()
		k.workers.Wait()
	}
	if k.closeDB != nil {
		k.closeDB()
	}
}
