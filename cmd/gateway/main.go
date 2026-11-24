// Command gateway runs Bifrost: MCP front door, REST API, SPA, the keyring
// and the housekeeping worker, all in one process.
//
// Every credential a server uses lives in the keyring, which also refreshes
// the OAuth tokens among them. `gateway migrate-credentials` is the one-off
// command that moves credentials an older gateway sealed in its own store.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/andyjmorgan/Bifrost/internal/audit"
	"github.com/andyjmorgan/Bifrost/internal/auth"
	"github.com/andyjmorgan/Bifrost/internal/cache"
	"github.com/andyjmorgan/Bifrost/internal/config"
	"github.com/andyjmorgan/Bifrost/internal/crypto"
	"github.com/andyjmorgan/Bifrost/internal/db"
	"github.com/andyjmorgan/Bifrost/internal/gateway"
	"github.com/andyjmorgan/Bifrost/internal/httpapi"
	"github.com/andyjmorgan/Bifrost/internal/keyring"
	"github.com/andyjmorgan/Bifrost/internal/oauthclient"
	"github.com/andyjmorgan/Bifrost/internal/refreshguard"
	"github.com/andyjmorgan/Bifrost/internal/store"
	"github.com/andyjmorgan/Bifrost/internal/upstream"
	"github.com/andyjmorgan/Bifrost/internal/worker"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	slog.SetDefault(log)
	cfg, err := config.FromEnv()
	if err != nil {
		log.Error("config", "err", err)
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if len(os.Args) > 1 && os.Args[1] == "create-key" {
		createKey(ctx, cfg, log, os.Args[2:])
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "migrate-credentials" {
		migrateCredentials(ctx, cfg, log, os.Args[2:])
		return
	}

	pool, err := db.Open(ctx, cfg.DSN)
	if err != nil {
		log.Error("database", "err", err)
		os.Exit(1)
	}
	defer pool.Close()
	st := store.NewPostgres(pool)
	warnUnmigrated(ctx, st, log)

	sealer, err := crypto.New(cfg.MasterKey)
	if err != nil {
		log.Error("crypto", "err", err)
		os.Exit(1)
	}
	keys := auth.NewKeys(st, sealer)
	authn := &auth.Authenticator{Keys: keys, Users: st, DevOwner: cfg.DevOwner, PublicBase: cfg.PublicBaseURL, MCPScope: cfg.OIDCScope, Issuer: cfg.OIDCIssuer}
	if cfg.DevMode() {
		log.Warn("DEV MODE: front-door auth disabled; every request runs as owner " + cfg.DevOwner)
	} else {
		oidc, err := auth.NewOIDC(ctx, cfg.OIDCIssuer, []string{cfg.OIDCWebClientID, cfg.PublicBaseURL + "/mcp"}, cfg.OIDCRequiredRole)
		if err != nil {
			log.Error("oidc", "err", err)
			os.Exit(1)
		}
		authn.OIDC = oidc
	}

	// The keyring is nil, and Wrap a no-op, unless its KEKs are configured.
	// It is built before the connector, which resolves every server's
	// credential through it.
	// Every token refresh the keyring makes holds this guard shared;
	// `gateway migrate-credentials` holds it exclusive while it moves rows.
	guard := refreshguard.New(pool)
	kr, err := keyring.FromEnv(ctx, keyring.HostConfig{
		DSN: cfg.DSN, PublicBaseURL: cfg.PublicBaseURL, OIDCIssuer: cfg.OIDCIssuer, OIDCWebClientID: cfg.OIDCWebClientID,
		RefreshGuard: guard,
	}, log)
	if err != nil {
		log.Error("keyring", "err", err)
		os.Exit(1)
	}
	if kr != nil {
		kr.Start()
		defer kr.Shutdown()
		log.Info("keyring enabled", "prefix", keyring.MountPrefix, "legacyHosts", kr.LegacyHosts())
	}

	rec := audit.NewRecorder(ctx, st, log)
	oauth := oauthclient.New(cfg.PublicBaseURL, &http.Client{Timeout: 20 * time.Second})
	conn := upstream.New(st, kr.Source(), rec, log, cfg.UpstreamTimeout, cfg.Version)
	cw := cache.NewWriter(ctx, st, log)
	conn.OnInit = func(srv *store.Server, ir *mcp.InitializeResult) {
		id := srv.ID
		cw.Put(cache.NSInit, id.String(), &id, srv.UpdatedAt, gateway.InitFrom(ir), cfg.ToolsCacheTTL)
	}
	gw := gateway.New(st, conn, rec, cw, cfg.ToolsCacheTTL, log, cfg.Version)
	gw.SetMetaAllow(cfg.MetaAllowIn, cfg.MetaAllowOut)
	gw.SetRuleHeaders(cfg.RuleHeaders)
	gw.SetLegacyProtocolVersions(cfg.LegacyProtocolVersions)
	if len(cfg.LegacyProtocolVersions) > 0 {
		log.Info("front door also accepts older protocol versions", "versions", cfg.LegacyProtocolVersions)
	}

	selfBase := "http://127.0.0.1" + cfg.ListenAddr
	if !strings.HasPrefix(cfg.ListenAddr, ":") {
		selfBase = "http://" + cfg.ListenAddr
	}
	api := httpapi.New(httpapi.Deps{
		Store: st, Keys: keys, Authn: authn, Conn: conn, OAuth: oauth, Keyring: kr.Source(), Gateway: gw, Recorder: rec, Cache: cw, ToolsTTL: cfg.ToolsCacheTTL, Log: log,
		PublicBase: cfg.PublicBaseURL, SelfBase: selfBase, WebRoot: cfg.WebRoot,
		Config: httpapi.ConfigDTO{DevMode: cfg.DevMode(), OIDCIssuer: cfg.OIDCIssuer, OIDCClientID: cfg.OIDCWebClientID,
			PublicBaseURL: cfg.PublicBaseURL, Version: cfg.Version, MCPScope: cfg.OIDCScope, AllScopes: auth.AllScopes},
	})

	go worker.New(st, log, cfg.RefreshInterval, cfg.AuditRetention).Run(ctx)

	srv := &http.Server{Addr: cfg.ListenAddr, Handler: kr.Wrap(api.Router()), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
	}()
	log.Info("listening", "addr", cfg.ListenAddr, "publicBase", cfg.PublicBaseURL, "devMode", cfg.DevMode(), "version", cfg.Version)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Error("server", "err", err)
		os.Exit(1)
	}
}

// unmigratedLister is the one question warnUnmigrated asks of the store.
type unmigratedLister interface {
	UnmigratedCredentials(ctx context.Context) ([]string, error)
}

// warnUnmigrated says, once at start, which servers still have their
// credential in the gateway's old store. This gateway does not read that
// store, so each of them fails every request until the one-off command has
// moved its row into the keyring. It logs nothing when there are none.
func warnUnmigrated(ctx context.Context, st unmigratedLister, log *slog.Logger) {
	slugs, err := st.UnmigratedCredentials(ctx)
	if err != nil {
		log.Error("could not check for credentials still in the gateway's old store", "err", err)
		return
	}
	if len(slugs) == 0 {
		return
	}
	log.Error("servers whose credential is still in the gateway's old store cannot be reached until it is moved to the keyring: "+
		"run `gateway migrate-credentials -kind static` and `gateway migrate-credentials -kind oauth`",
		"count", len(slugs), "servers", slugs)
}

// createKey is the bootstrap path: `gateway create-key -name <n> -user <subject> [-scopes a,b]`
// mints an API key straight into the database and prints the secret once.
// The key acts as the user, who is recorded if they have not signed in yet,
// so the first key can be made from inside the pod before anyone has.
func createKey(ctx context.Context, cfg config.Config, log *slog.Logger, args []string) {
	fs := flag.NewFlagSet("create-key", flag.ExitOnError)
	name := fs.String("name", "", "key name")
	scopes := fs.String("scopes", strings.Join(auth.AllScopes, ","), "comma-separated scopes")
	user := fs.String("user", "", "the identity provider subject the key acts as")
	_ = fs.Parse(args)
	if *name == "" || *user == "" {
		fs.Usage()
		os.Exit(2)
	}
	pool, err := db.Open(ctx, cfg.DSN)
	if err != nil {
		log.Error("database", "err", err)
		os.Exit(1)
	}
	defer pool.Close()
	sealer, err := crypto.New(cfg.MasterKey)
	if err != nil {
		log.Error("crypto", "err", err)
		os.Exit(1)
	}
	st := store.NewPostgres(pool)
	if _, err := st.GetUser(ctx, *user); errors.Is(err, store.ErrNotFound) {
		if _, err := st.EnsureUser(ctx, *user, "", ""); err != nil {
			log.Error("record user", "err", err)
			os.Exit(1)
		}
	}
	rec, secret, err := auth.NewKeys(st, sealer).Create(ctx, *user, *name, strings.Split(*scopes, ","))
	if err != nil {
		log.Error("create key", "err", err)
		os.Exit(1)
	}
	log.Info("api key created", "id", rec.ID, "prefix", rec.Prefix, "user", rec.CreatedBy, "scopes", rec.Scopes)
	fmt.Println(secret)
}
