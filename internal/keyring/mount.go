package keyring

import (
	"net/http"
	"strings"
)

// legacyPagePaths maps a page of Vault's SPA to the page of the gateway's SPA that replaced it.
// The root and any path that is not listed go to uiLandingPath. Read-only after init.
var legacyPagePaths = map[string]string{
	"/credentials":  uiLandingPath, // its "tab" query parameter is carried over
	"/providers":    "/providers",
	"/oauthconnect": uiConnectPath,
	"/profile":      "/profile",
}

// Wrap puts the keyring in front of next, the gateway's own router, and returns the handler to
// serve. The keyring is checked first, because it shares paths with the gateway:
//
//   - /keyring/api/… and /keyring/healthz, on every host, are the keyring's API with the prefix
//     removed. Anything else under /keyring belongs to next.
//   - On a legacy host the keyring answers at Vault's original paths. /api/… is always served by
//     the keyring and never falls through, although the gateway defines /api/v1/api-keys,
//     /api/v1/audit and /api/v1/me too. Any other GET or HEAD is a browser loading a page, and is
//     redirected to the gateway's UI. API calls are never redirected: an HTTP client drops the
//     Authorization header when a redirect changes host.
//
// On a nil Keyring it returns next unchanged.
func (k *Keyring) Wrap(next http.Handler) http.Handler {
	if k == nil {
		return next
	}
	prefixed := http.StripPrefix(MountPrefix, k.api)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rest, underPrefix := strings.CutPrefix(r.URL.Path, MountPrefix)
		switch {
		case underPrefix && isAPIPath(rest):
			prefixed.ServeHTTP(w, r)
		case matchHost(k.cfg.LegacyHosts, r.Host):
			k.serveLegacyHost(w, r)
		default:
			next.ServeHTTP(w, r)
		}
	})
}

// serveLegacyHost answers a request that arrived on one of Vault's old hosts.
func (k *Keyring) serveLegacyHost(w http.ResponseWriter, r *http.Request) {
	if isAPIPath(r.URL.Path) {
		k.api.ServeHTTP(w, r)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.NotFound(w, r)
		return
	}
	// A temporary redirect that is never cached: pointing the host back at Vault must take effect
	// at once.
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, k.pageURL(r.URL.Path, r.URL.RawQuery), http.StatusFound) //nolint:gosec // G710: pageURL joins the configured base to a path from a fixed map; the request supplies only a query string
}

// pageURL is the gateway UI address that stands in for a page of Vault's SPA. The target is built
// from the configured base and a fixed path, never from the request's host, so it cannot be steered
// off site.
func (k *Keyring) pageURL(path, rawQuery string) string {
	page, ok := legacyPagePaths[strings.TrimRight(path, "/")]
	if !ok {
		// The root and any unknown page keep nothing of the old address, query included: the root
		// is where Vault's own sign-in returned with a one-time code, which is not the gateway's.
		return k.cfg.UIBaseURL + uiLandingPath
	}
	if rawQuery != "" {
		page += "?" + rawQuery
	}
	return k.cfg.UIBaseURL + page
}

// isAPIPath reports whether a path, in Vault's own layout, belongs to the API rather than the SPA.
func isAPIPath(path string) bool {
	return path == "/healthz" || path == "/api" || strings.HasPrefix(path, "/api/")
}
