// Command gateway runs the mcp-gateway: MCP front door, REST API, SPA and
// the token-refresh worker, all in one process.
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

	"github.com/andyjmorgan/mcp-gateway/internal/audit"
	"github.com/andyjmorgan/mcp-gateway/internal/auth"
	"github.com/andyjmorgan/mcp-gateway/internal/cache"
	"github.com/andyjmorgan/mcp-gateway/internal/config"
	"github.com/andyjmorgan/mcp-gateway/internal/crypto"
	"github.com/andyjmorgan/mcp-gateway/internal/db"
	"github.com/andyjmorgan/mcp-gateway/internal/gateway"
	"github.com/andyjmorgan/mcp-gateway/internal/httpapi"
	"github.com/andyjmorgan/mcp-gateway/internal/oauthclient"
	"github.com/andyjmorgan/mcp-gateway/internal/store"
	"github.com/andyjmorgan/mcp-gateway/internal/upstream"
	"github.com/andyjmorgan/mcp-gateway/internal/worker"
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

	pool, err := db.Open(ctx, cfg.DSN)
	if err != nil {
		log.Error("database", "err", err)
		os.Exit(1)
	}
	defer pool.Close()
	st := store.NewPostgres(pool)

	sealer, err := crypto.New(cfg.MasterKey)
	if err != nil {
		log.Error("crypto", "err", err)
		os.Exit(1)
	}
	keys := auth.NewKeys(st, sealer)
	authn := &auth.Authenticator{Keys: keys, DevOwner: cfg.DevOwner, PublicBase: cfg.PublicBaseURL, MCPScope: cfg.OIDCScope, Issuer: cfg.OIDCIssuer, OwnerID: cfg.OwnerID}
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

	rec := audit.NewRecorder(ctx, st, log)
	oauth := oauthclient.New(cfg.PublicBaseURL, &http.Client{Timeout: 20 * time.Second})
	conn := upstream.New(st, sealer, oauth, rec, log, cfg.UpstreamTimeout, cfg.RefreshLeeway, cfg.Version)
	cw := cache.NewWriter(ctx, st, log)
	conn.OnInit = func(srv *store.Server, ir *mcp.InitializeResult) {
		in := cache.Init{ProtocolVersion: ir.ProtocolVersion, Instructions: ir.Instructions}
		if ir.ServerInfo != nil {
			in.ServerName, in.ServerVersion, in.Title = ir.ServerInfo.Name, ir.ServerInfo.Version, ir.ServerInfo.Title
		}
		id := srv.ID
		cw.Put(cache.NSInit, id.String(), &id, in, cfg.ToolsCacheTTL)
	}
	gw := gateway.New(st, conn, rec, cw, cfg.ToolsCacheTTL, log, cfg.Version)

	selfBase := "http://127.0.0.1" + cfg.ListenAddr
	if !strings.HasPrefix(cfg.ListenAddr, ":") {
		selfBase = "http://" + cfg.ListenAddr
	}
	api := httpapi.New(httpapi.Deps{
		Store: st, Keys: keys, Authn: authn, Conn: conn, OAuth: oauth, Gateway: gw, Recorder: rec, Cache: cw, ToolsTTL: cfg.ToolsCacheTTL, Log: log,
		PublicBase: cfg.PublicBaseURL, SelfBase: selfBase, WebRoot: cfg.WebRoot,
		Config: httpapi.ConfigDTO{DevMode: cfg.DevMode(), OIDCIssuer: cfg.OIDCIssuer, OIDCClientID: cfg.OIDCWebClientID,
			PublicBaseURL: cfg.PublicBaseURL, Version: cfg.Version, MCPScope: cfg.OIDCScope, AllScopes: auth.AllScopes},
	})

	go worker.New(st, conn, log, cfg.RefreshInterval, cfg.RefreshLeeway, cfg.AuditRetention).Run(ctx)

	srv := &http.Server{Addr: cfg.ListenAddr, Handler: api.Router(), ReadHeaderTimeout: 10 * time.Second}
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

// createKey is the bootstrap path: `gateway create-key -name <n> [-scopes a,b] [-owner id]`
// mints an API key straight into the database and prints the secret once.
// It exists so the first key can be made from inside the pod before any
// human has signed in.
func createKey(ctx context.Context, cfg config.Config, log *slog.Logger, args []string) {
	fs := flag.NewFlagSet("create-key", flag.ExitOnError)
	name := fs.String("name", "", "key name")
	scopes := fs.String("scopes", strings.Join(auth.AllScopes, ","), "comma-separated scopes")
	owner := fs.String("owner", cfg.OwnerID, "owner id the key belongs to")
	_ = fs.Parse(args)
	if *name == "" {
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
	rec, secret, err := auth.NewKeys(store.NewPostgres(pool), sealer).Create(ctx, *owner, *name, strings.Split(*scopes, ","))
	if err != nil {
		log.Error("create key", "err", err)
		os.Exit(1)
	}
	log.Info("api key created", "id", rec.ID, "prefix", rec.Prefix, "owner", rec.OwnerID, "scopes", rec.Scopes)
	fmt.Println(secret)
}
