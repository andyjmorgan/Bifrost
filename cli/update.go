package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/andyjmorgan/Bifrost/cli/internal/identity"
	"github.com/andyjmorgan/Bifrost/cli/internal/selfupdate"
)

// checkInterval is how long a passive update check stays fresh before the next refresh.
const checkInterval = 24 * time.Hour

// runUpdate performs the in-place upgrade: resolve the latest release, and unless it's
// already current (or force is set), download+verify it and replace this binary. checkOnly
// reports availability without installing. All output goes to w (STDERR in production).
//
// Two outcomes change nothing and are not errors, since nothing that was asked can be done:
// the name does not update itself (`dwvault`), or no release has been published yet.
func runUpdate(w io.Writer, force, checkOnly bool) error {
	id := identity.Current()
	if !id.SelfUpdate {
		// A release is a bifrost binary. Fetched under this name it would replace whatever
		// the name points at with something its user did not ask for, so nothing is fetched.
		_, _ = fmt.Fprintf(w, "%s is the compatibility name for bifrost and does not update itself, so nothing was changed.\n"+
			"Run `bifrost update` to update the binary this name points at.\n", id.Name)
		return nil
	}
	if !selfupdate.IsRelease(version) && !force {
		_, _ = fmt.Fprintf(w, "%s: built from source (version %q); nothing to update — use --force to install the latest release anyway\n", id.Name, version)
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	rel, err := selfupdate.Latest(ctx)
	if errors.Is(err, selfupdate.ErrNoRelease) {
		// Remember the answer so the passive check does not ask again on every command.
		_ = selfupdate.SaveState(selfupdate.State{CheckedAt: time.Now()})
		_, _ = fmt.Fprintf(w, "%s: no release has been published yet at %s (releases are tagged %svX.Y.Z), so there is nothing to update to. Nothing was changed.\n",
			id.Name, releasePage(), selfupdate.TagPrefix)
		return nil
	}
	if err != nil {
		return fmt.Errorf("check for updates: %w", err)
	}
	latest := rel.Version()

	// Refresh the passive-notice cache while we have a fresh answer.
	_ = selfupdate.SaveState(selfupdate.State{CheckedAt: time.Now(), LatestVersion: latest})

	newer := selfupdate.Compare(latest, version) > 0
	if checkOnly {
		if newer {
			_, _ = fmt.Fprintf(w, "%s %s is available (you have %s) — run `%s update` to upgrade\n", id.Name, latest, version, id.Name)
		} else {
			_, _ = fmt.Fprintf(w, "%s %s is the latest release (you have %s)\n", id.Name, latest, version)
		}
		return nil
	}
	if !newer && !force {
		_, _ = fmt.Fprintf(w, "%s %s is up to date\n", id.Name, version)
		return nil
	}

	_, _ = fmt.Fprintf(w, "downloading %s %s (%s)…\n", id.Name, latest, selfupdate.AssetName())
	bin, err := selfupdate.Download(ctx, rel)
	if err != nil {
		return fmt.Errorf("download update: %w", err)
	}
	path, err := selfupdate.Apply(bin)
	if err != nil {
		return fmt.Errorf("install update: %w", err)
	}
	_, _ = fmt.Fprintf(w, "updated %s %s → %s (%s)\n", id.Name, version, latest, path)
	warnIfNotOnPath(w, path)
	return nil
}

// releasePage names where releases are published, for messages. An unusable BIFROST_REPO
// has already been reported as an error by then, so the default is a safe fallback here.
func releasePage() string {
	repo, err := selfupdate.Repo()
	if err != nil {
		repo = selfupdate.DefaultRepo
	}
	return "github.com/" + repo + "/releases"
}

// warnIfNotOnPath advises the user when the (in-place) binary lives in a directory that
// isn't on $PATH — the one case the in-place update can't fix on its own. Mirrors the PATH
// hint install.sh prints. Advisory only: it never edits PATH or any shell profile.
func warnIfNotOnPath(w io.Writer, path string) {
	dir := filepath.Dir(path)
	for _, p := range filepath.SplitList(os.Getenv("PATH")) {
		if p == dir {
			return
		}
	}
	_, _ = fmt.Fprintf(w,
		"\nnote: %s is not on your PATH. Add it, e.g.:\n  echo 'export PATH=\"%s:$PATH\"' >> ~/.profile && . ~/.profile\n",
		dir, dir)
}

func cmdUpdate() *cobra.Command {
	var check, force bool
	c := &cobra.Command{
		Use:   "update",
		Short: "Upgrade " + name() + " to the latest release (in place)",
		Long: "Download the latest " + name() + " release for this platform, verify its checksum, and\n" +
			"replace the running binary in place. PATH is not modified — the binary is updated\n" +
			"wherever it already lives.\n\n" +
			"Releases come from GitHub (" + selfupdate.DefaultRepo + ", tags " + selfupdate.TagPrefix + "vX.Y.Z); set\n" +
			selfupdate.RepoEnv + "=owner/name to follow another repository. When no release has been\n" +
			"published, this command says so and changes nothing. It also changes nothing when\n" +
			"run as `dwvault`.",
		Args: cobra.NoArgs,
		// Still runnable under a name that cannot update, so a script calling it gets the
		// explanation rather than "unknown command"; just not advertised there.
		Hidden: !identity.Current().SelfUpdate,
		RunE: func(_ *cobra.Command, _ []string) error {
			return runUpdate(os.Stderr, force, check)
		},
	}
	c.Flags().BoolVar(&check, "check", false, "report whether an update is available, without installing")
	c.Flags().BoolVar(&force, "force", false, "reinstall the latest release even if already current")
	return c
}

// cmdUpdateCheckHidden is the detached worker spawned by the passive check: it fetches the
// latest version and refreshes the cache, then exits 0. It is silent and never user-facing.
func cmdUpdateCheckHidden() *cobra.Command {
	return &cobra.Command{
		Use:    "__update-check",
		Hidden: true,
		Args:   cobra.NoArgs,
		// Bypass the passive hook on the parent (it's already skipped for this name, but be
		// explicit) and never error out — this is best-effort background work.
		RunE: func(_ *cobra.Command, _ []string) error {
			if !identity.Current().SelfUpdate {
				return nil
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			rel, err := selfupdate.Latest(ctx)
			switch {
			case errors.Is(err, selfupdate.ErrNoRelease):
				// Nothing published is an answer too; cache it for the interval.
				_ = selfupdate.SaveState(selfupdate.State{CheckedAt: time.Now()})
			case err == nil:
				_ = selfupdate.SaveState(selfupdate.State{CheckedAt: time.Now(), LatestVersion: rel.Version()})
			}
			// Any other error is swallowed: a failed background check just leaves the cache stale.
			return nil
		},
	}
}

// updateCheckDisabled reports whether one of the identity's opt-out variables is set.
func updateCheckDisabled(id identity.Identity) bool {
	for _, k := range id.NoUpdateCheckEnv {
		if os.Getenv(k) != "" {
			return true
		}
	}
	return false
}

// maybeNotifyUpdate is the passive, best-effort update notice run before every command. It
// adds no network latency to the real command: it only reads a cached version file (printing
// a one-line STDERR notice when a newer release is known) and, if the cache is stale, spawns
// a detached child to refresh it for next time. It is a no-op for builds from source, when
// opted out, under a name that does not update itself, for the update commands themselves,
// and when STDERR is not a terminal (keeps scripts and agents — whose STDOUT carries
// secrets — free of noise).
func maybeNotifyUpdate(cmd *cobra.Command) {
	id := identity.Current()
	if !id.SelfUpdate || !selfupdate.IsRelease(version) || doUpdate || updateCheckDisabled(id) {
		return
	}
	switch cmd.Name() {
	case "update", "__update-check", "help", "completion", "version":
		return
	}
	if cmd.Parent() != nil && cmd.Parent().Name() == "completion" {
		return
	}
	if !term.IsTerminal(int(os.Stderr.Fd())) {
		return
	}

	state, err := selfupdate.LoadState()
	if err != nil {
		return // cache dir unreadable — stay silent rather than spawn a child every run
	}
	if state.LatestVersion != "" && selfupdate.Compare(state.LatestVersion, version) > 0 {
		fmt.Fprintf(os.Stderr, "%s: a newer version is available: %s (you have %s) — run `%s update`\n",
			id.Name, state.LatestVersion, version, id.Name)
	}
	if time.Since(state.CheckedAt) > checkInterval {
		spawnBackgroundCheck()
	}
}

// spawnBackgroundCheck launches `bifrost __update-check` fully detached (own session, all
// std streams to /dev/null) and returns immediately without waiting. Any failure to launch
// is ignored — the notice simply lags by one invocation.
func spawnBackgroundCheck() {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	devnull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return
	}
	defer func() { _ = devnull.Close() }()
	c := exec.Command(exe, "__update-check") //nolint:gosec // G204: exe is os.Executable() (this binary's own path), not attacker-supplied
	c.Stdin, c.Stdout, c.Stderr = devnull, devnull, devnull
	c.SysProcAttr = &syscall.SysProcAttr{Setsid: true} // outlive the parent process
	if err := c.Start(); err == nil {
		_ = c.Process.Release()
	}
}
