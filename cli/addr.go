package main

import (
	"fmt"
	"strings"

	"github.com/andyjmorgan/Bifrost/cli/internal/identity"
)

// defaultAddr is the server address when --addr is not given: the first of the identity's
// address variables that is set (BIFROST_ADDR, then VAULT_ADDR; only VAULT_ADDR as
// `dwvault`), otherwise the identity's built-in default. An explicit --addr overrides it.
func defaultAddr(id identity.Identity, getenv func(string) string) string {
	for _, k := range id.AddrEnv {
		if v := strings.TrimSpace(getenv(k)); v != "" {
			return v
		}
	}
	return id.DefaultAddr
}

// addrUsage is the --addr help line. It names the variables that supply the address; the
// value in effect is appended by the flag library as the default.
func addrUsage(id identity.Identity) string {
	return fmt.Sprintf("server address (https://host[:port][/prefix] or host:port); also set by %s",
		strings.Join(id.AddrEnv, ", then "))
}

// baseURL turns an address into the REST base URL, which is also the key the login is
// stored under. The scheme is the sole transport signal: an http(s):// address is used
// verbatim; a bare host[:port] with no scheme defaults to plaintext http://, so give an
// https:// address (or rely on the default) for TLS.
//
// A path is kept as a prefix: every request goes to <base>/api/..., so
// https://bifrost.example/keyring and https://vault.example are both valid bases. Trailing
// slashes are dropped so the two spellings of one base share a stored login.
func baseURL(a string) string {
	a = strings.TrimRight(strings.TrimSpace(a), "/")
	switch {
	case strings.HasPrefix(a, "http://"), strings.HasPrefix(a, "https://"):
		return a
	default:
		return "http://" + a
	}
}

// httpBaseURL is the REST base URL for this invocation, from --addr or its default.
func httpBaseURL() string { return baseURL(addr) }
