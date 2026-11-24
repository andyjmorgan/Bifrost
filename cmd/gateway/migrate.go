package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/google/uuid"

	"github.com/andyjmorgan/Bifrost/internal/config"
	"github.com/andyjmorgan/Bifrost/internal/credmigrate"
	"github.com/andyjmorgan/Bifrost/internal/crypto"
	"github.com/andyjmorgan/Bifrost/internal/db"
	"github.com/andyjmorgan/Bifrost/internal/keyring"
	"github.com/andyjmorgan/Bifrost/internal/keyringref"
	"github.com/andyjmorgan/Bifrost/internal/refreshguard"
)

// migrateCredentials is `gateway migrate-credentials`: the one-off move of
// the credentials the gateway sealed in its own store into the keyring, and
// back. It runs in a process that holds both keys (the gateway's master key
// and the keyring's KEKs), which is why it is a subcommand of the gateway
// and not a SQL migration. It reads the same environment as the server.
//
// The report goes to stdout, one line for each server, names only. It exits
// 1 when a credential could not be moved, and 2 on a usage or setup error.
func migrateCredentials(ctx context.Context, cfg config.Config, log *slog.Logger, args []string) {
	fs := flag.NewFlagSet("migrate-credentials", flag.ExitOnError)
	kind := fs.String("kind", "", "what to move: static (Basic and header credentials) or oauth (OAuth registrations and tokens)")
	dryRun := fs.Bool("dry-run", false, "print what would be moved, names only, and change nothing")
	rollback := fs.Bool("rollback", false, "move the credentials this command moved back into the gateway's own store")
	owner := fs.String("owner", "gateway", `the keyring user that owns the moved credentials: "gateway" (the gateway's own space, which no person can read) or a user's id`)
	includeNew := fs.Bool("include-new", false, "with -rollback: also take back credentials the gateway has stored in the keyring since the move")
	servers := fs.String("server", "", "comma-separated slugs to limit the run to; empty means every server")
	ignoreSessions := fs.Bool("ignore-other-sessions", false, "with -kind oauth: go ahead although sessions that are not this version of Bifrost are connected to the database (only when you know they are not an older gateway)")
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "usage: gateway migrate-credentials -kind static|oauth [-dry-run] [-rollback] [-owner gateway|<user id>] [-server slug,slug] [-include-new] [-ignore-other-sessions]")
		fs.PrintDefaults()
	}
	_ = fs.Parse(args)

	ownerID, err := uuid.Parse(keyringref.GatewayOwner)
	if *owner != "gateway" {
		ownerID, err = uuid.Parse(*owner)
	}
	if err != nil || ownerID == uuid.Nil || (*kind != credmigrate.KindStatic && *kind != credmigrate.KindOAuth) {
		fs.Usage()
		os.Exit(2)
	}
	if *includeNew && !*rollback {
		fmt.Fprintln(fs.Output(), "-include-new only applies to -rollback")
		os.Exit(2)
	}

	pool, err := db.Open(ctx, cfg.DSN)
	if err != nil {
		log.Error("database", "err", err)
		os.Exit(2)
	}
	defer pool.Close()
	sealer, err := crypto.New(cfg.MasterKey)
	if err != nil {
		log.Error("crypto", "err", err)
		os.Exit(2)
	}
	guard := refreshguard.New(pool)
	kr, err := keyring.FromEnv(ctx, keyring.HostConfig{
		DSN: cfg.DSN, PublicBaseURL: cfg.PublicBaseURL, OIDCIssuer: cfg.OIDCIssuer, OIDCWebClientID: cfg.OIDCWebClientID,
	}, log)
	if err != nil {
		log.Error("keyring", "err", err)
		os.Exit(2)
	}
	if kr == nil {
		log.Error("the keyring is not enabled: set its KEKs (MCPGW_KEYRING_CRYPTO_ACTIVE_KEK_ID and MCPGW_KEYRING_CRYPTO_KEKS) as the server has them")
		os.Exit(2)
	}
	// The audit writer runs so that what the command reads and writes in the
	// keyring is on record; Shutdown flushes it.
	kr.Start()

	var slugs []string
	for _, s := range strings.Split(*servers, ",") {
		if s = strings.TrimSpace(s); s != "" {
			slugs = append(slugs, s)
		}
	}
	_, err = credmigrate.Run(ctx, credmigrate.Deps{Pool: pool, Sealer: sealer, Vault: kr.Migration(), Guard: guard}, credmigrate.Options{
		Kind: *kind, DryRun: *dryRun, Rollback: *rollback, Owner: ownerID, IncludeNew: *includeNew, Servers: slugs, IgnoreOtherSessions: *ignoreSessions, Out: os.Stdout,
	})
	kr.Shutdown()
	pool.Close()
	switch {
	case errors.Is(err, credmigrate.ErrFailed):
		os.Exit(1)
	case err != nil:
		log.Error("migrate-credentials", "err", err)
		os.Exit(2)
	}
}
