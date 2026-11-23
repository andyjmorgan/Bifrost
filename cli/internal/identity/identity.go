// Package identity resolves the name the CLI runs under and everything that follows from
// that name: the default server address, the environment variables consulted, and where
// the login is stored.
//
// The binary is `bifrost`. Started as `dwvault` (argv[0], typically through a symlink) it
// behaves as the DonkeyWork Vault CLI it replaces, with that CLI's default address and
// storage names, so scripts written against `dwvault` keep working unchanged.
package identity

import (
	"os"
	"path/filepath"
	"strings"
)

const (
	bifrostName = "bifrost"
	dwvaultName = "dwvault"

	// BifrostAddr is the keyring API as Bifrost serves it: the Vault API under a path prefix.
	BifrostAddr = "https://bifrost.donkeywork.dev/keyring"
	// VaultAddr is the original Vault host, which keeps serving the same API at the root.
	VaultAddr = "https://vault.donkeywork.dev"
)

// Identity is the set of names one invocation of the CLI answers to.
type Identity struct {
	// Name is the command name used in help text, messages and release asset names.
	Name string
	// Product is what the command fronts, for help text.
	Product string
	// DefaultAddr is the server base URL used when neither --addr nor an AddrEnv variable is set.
	DefaultAddr string
	// AddrEnv lists the environment variables that supply the address, highest precedence first.
	AddrEnv []string
	// NoUpdateCheckEnv lists the environment variables that switch off the passive update notice.
	NoUpdateCheckEnv []string
	// ConfigDir is the directory under the user config dir holding hosts.json, the 0600
	// credentials file and the update-check cache.
	ConfigDir string
	// KeyringService is the OS keyring service name the login is stored under.
	KeyringService string
	// SelfUpdate reports whether this name may replace its own binary.
	SelfUpdate bool
	// Legacy names an older CLI whose stored login is read when this one has none. Nil when
	// there is no predecessor to read from.
	Legacy *Legacy
}

// Legacy describes a predecessor's storage. It is only ever read.
type Legacy struct {
	// Name is the predecessor's command name, for messages.
	Name string
	// ConfigDir is the predecessor's directory under the user config dir.
	ConfigDir string
	// KeyringService is the predecessor's OS keyring service name.
	KeyringService string
	// HostAliases maps a server base URL of this identity to the base URL the predecessor
	// stored the same server's login under.
	HostAliases map[string]string
}

// Bifrost is the identity of the CLI under its own name.
func Bifrost() Identity {
	return Identity{
		Name:             bifrostName,
		Product:          "Bifrost",
		DefaultAddr:      BifrostAddr,
		AddrEnv:          []string{"BIFROST_ADDR", "VAULT_ADDR"},
		NoUpdateCheckEnv: []string{"BIFROST_NO_UPDATE_CHECK", "VAULT_NO_UPDATE_CHECK"},
		ConfigDir:        bifrostName,
		KeyringService:   bifrostName,
		SelfUpdate:       true,
		Legacy: &Legacy{
			Name:           dwvaultName,
			ConfigDir:      dwvaultName,
			KeyringService: dwvaultName,
			// The two hosts are one server, so a login made against the Vault host is the
			// login for the keyring on the Bifrost host.
			HostAliases: map[string]string{BifrostAddr: VaultAddr},
		},
	}
}

// Dwvault is the identity of the CLI under its compatibility name: the address, environment
// variables and storage names of the DonkeyWork Vault CLI, and no self-update, because an
// update fetched under this name would replace the binary with one of a different name.
func Dwvault() Identity {
	return Identity{
		Name:             dwvaultName,
		Product:          "DonkeyWork Vault",
		DefaultAddr:      VaultAddr,
		AddrEnv:          []string{"VAULT_ADDR"},
		NoUpdateCheckEnv: []string{"VAULT_NO_UPDATE_CHECK"},
		ConfigDir:        dwvaultName,
		KeyringService:   dwvaultName,
	}
}

// FromArgv0 picks the identity for the name the process was started as. Only the exact
// compatibility name selects it; any other name, a renamed or versioned binary included,
// is Bifrost.
func FromArgv0(argv0 string) Identity {
	base := strings.TrimSuffix(filepath.Base(argv0), ".exe")
	if base == dwvaultName {
		return Dwvault()
	}
	return Bifrost()
}

// LegacyHosts returns the base URLs under which the predecessor may hold a login for host:
// host itself, then its alias if it has one. It is empty when there is no predecessor.
func (id Identity) LegacyHosts(host string) []string {
	if id.Legacy == nil {
		return nil
	}
	hosts := []string{host}
	if alias, ok := id.Legacy.HostAliases[host]; ok && alias != host {
		hosts = append(hosts, alias)
	}
	return hosts
}

// current is fixed from argv[0] at start-up and read-only afterwards. It is a var only so
// tests can run under either name through SetForTesting; production never reassigns it.
var current = fromArgs(os.Args)

func fromArgs(args []string) Identity {
	if len(args) == 0 {
		return Bifrost()
	}
	return FromArgv0(args[0])
}

// Current returns the identity this process runs under.
func Current() Identity { return current }

// SetForTesting swaps the process identity and returns a function that restores it. It
// exists for tests only.
func SetForTesting(id Identity) (restore func()) {
	old := current
	current = id
	return func() { current = old }
}
