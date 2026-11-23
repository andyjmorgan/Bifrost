// Package restclient is a minimal HTTP client for the keyring REST API. It currently
// covers the small hand-written calls used before the generated client can be built
// with an authenticated request editor.
package restclient

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/andyjmorgan/Bifrost/cli/internal/identity"
)

// Me is the identity behind a credential, from GET /api/v1/me.
type Me struct {
	UserID   string `json:"userId"`
	TenantID string `json:"tenantId"`
	Email    string `json:"email"`
	Name     string `json:"name"`
}

// FetchMe validates an API key against baseURL and returns the caller identity.
// A non-2xx response is an error, so it doubles as key validation.
func FetchMe(baseURL, apiKey string) (*Me, error) {
	return fetchMe(baseURL, func(req *http.Request) {
		req.Header.Set("X-Api-Key", apiKey)
	})
}

// FetchMeBearer validates a bearer token against baseURL and returns the caller identity.
func FetchMeBearer(baseURL, token string) (*Me, error) {
	return fetchMe(baseURL, func(req *http.Request) {
		req.Header.Set("Authorization", "Bearer "+token)
	})
}

// AppConfig is the server's public client configuration, from GET /api/config.
type AppConfig struct {
	Authority            string `json:"authority"`
	ClientID             string `json:"clientId"`
	Scopes               string `json:"scopes"`
	AuthEnabled          bool   `json:"authEnabled"`
	CliClientID          string `json:"cliClientId"`
	CliScopes            string `json:"cliScopes"`
	RequireHTTPSMetadata bool   `json:"requireHttpsMetadata"`
}

// FetchConfig retrieves the server's public client configuration from baseURL.
func FetchConfig(baseURL string) (*AppConfig, error) {
	u := strings.TrimRight(baseURL, "/") + "/api/config"
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("GET %s: %s", u, resp.Status)
	}
	var cfg AppConfig
	if err := json.Unmarshal(body, &cfg); err != nil {
		if IsWebPage(body) {
			return nil, notTheAPI(u)
		}
		return nil, fmt.Errorf("parse /api/config response: %w", err)
	}
	return &cfg, nil
}

// IsWebPage reports whether body is markup rather than JSON. A web app that serves its page
// for every unknown path answers 200 with HTML where the API was expected, which is what a
// wrong address, or one the API is not served at yet, looks like.
func IsWebPage(body []byte) bool {
	return bytes.HasPrefix(bytes.TrimLeft(body, " \t\r\n"), []byte("<"))
}

func notTheAPI(u string) error {
	return fmt.Errorf("GET %s answered with a web page, not the API; check the address (--addr)", u)
}

// PostForm posts the given values as an application/x-www-form-urlencoded body to endpoint
// and returns the response body and status code.
func PostForm(endpoint string, values map[string]string) ([]byte, int, error) {
	form := url.Values{}
	for k, v := range values {
		form.Set(k, v)
	}
	req, err := http.NewRequest(http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", identity.Current().Name)
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return body, resp.StatusCode, nil
}

func fetchMe(baseURL string, edit func(*http.Request)) (*Me, error) {
	u := strings.TrimRight(baseURL, "/") + "/api/v1/me"
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	edit(req)
	req.Header.Set("Accept", "application/json")

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))

	switch {
	case resp.StatusCode == http.StatusUnauthorized, resp.StatusCode == http.StatusForbidden:
		return nil, fmt.Errorf("key rejected by %s (%s)", u, resp.Status)
	case resp.StatusCode/100 != 2:
		return nil, fmt.Errorf("GET %s: %s", u, resp.Status)
	}

	var m Me
	if err := json.Unmarshal(body, &m); err != nil {
		if IsWebPage(body) {
			return nil, notTheAPI(u)
		}
		return nil, fmt.Errorf("parse /me response: %w", err)
	}
	return &m, nil
}
