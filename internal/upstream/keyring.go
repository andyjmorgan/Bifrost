package upstream

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/andyjmorgan/Bifrost/internal/audit"
	"github.com/andyjmorgan/Bifrost/internal/keyringref"
)

// keyringMemo is what the keyring handed out for one HTTP client, which is
// one upstream session: the handful of requests (initialize, the call, the
// close) that carry out a single forwarded request.
//
// Nothing here outlives that client, and an access token is dropped at its
// own expiry even within it. The next forwarded request asks the keyring
// again, so a credential that was changed or deleted takes effect then, and
// the keyring's audit trail gets one entry for each forwarded request
// rather than one for each HTTP round trip.
type keyringMemo struct {
	mu     sync.Mutex
	token  *keyringref.Token
	header *keyringref.Header
}

// use is how the keyring is told which server a fetch is for.
func (t *credentialTransport) use() keyringref.Use {
	return keyringref.Use{ServerID: t.srv.ID.String(), Slug: t.srv.Slug}
}

// keyringToken returns the access token for this client's OAuth account. A
// non-empty rejected is the token the server has just refused: the memo is
// bypassed and the keyring is asked to replace it.
func (t *credentialTransport) keyringToken(req *http.Request, rejected string) (keyringref.Token, error) {
	t.kr.mu.Lock()
	defer t.kr.mu.Unlock()
	if tok := t.kr.token; tok != nil && rejected == "" && (tok.ExpiresAt == nil || time.Now().Before(*tok.ExpiresAt)) {
		return *tok, nil
	}
	// A token another request on this client already replaced is not asked
	// for twice.
	if tok := t.kr.token; tok != nil && rejected != "" && tok.AccessToken != rejected {
		return *tok, nil
	}
	t.kr.token = nil
	// The fetch may refresh the token, and a refresh outlives the caller: a
	// cancelled request must not abort a rotation half-way, when the old
	// refresh token may already be spent.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(req.Context()), t.c.timeout)
	defer cancel()
	tok, err := t.c.keyring.OAuthToken(ctx, t.cred.KeyringRef(), t.use(), rejected)
	if err != nil {
		return keyringref.Token{}, err
	}
	t.kr.token = &tok
	return tok, nil
}

// keyringOAuth sends the request with a live access token of the keyring
// OAuth account the server references. A 401 makes the keyring refresh the
// token once, and the request is sent once more with the new one.
func (t *credentialTransport) keyringOAuth(req *http.Request) (*http.Response, error) {
	tok, err := t.keyringToken(req, "")
	if err != nil {
		return nil, t.keyringErr(err)
	}
	req = cloneWithBody(req)
	req.Header.Set("Authorization", "Bearer "+tok.AccessToken)
	resp, err := t.next.RoundTrip(req)
	if err != nil || resp.StatusCode != http.StatusUnauthorized {
		return resp, err
	}
	// The token was rejected before its expiry: have the keyring replace it.
	fresh, err := t.keyringToken(req, tok.AccessToken)
	if errors.Is(err, keyringref.ErrNoRefresh) {
		// Nothing to refresh with: the server's answer stands.
		t.c.log.Warn("upstream: keyring token rejected and it cannot be refreshed", "slug", t.srv.Slug, "credential", t.cred.KeyringRef().String())
		return resp, nil
	}
	io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16)) //nolint:errcheck
	resp.Body.Close()
	if err != nil {
		return nil, t.keyringErr(err)
	}
	t.c.log.Info("upstream: keyring token rejected; retrying with a refreshed one", "slug", t.srv.Slug)
	retry := cloneWithBody(req)
	retry.Header.Set("Authorization", "Bearer "+fresh.AccessToken)
	return t.next.RoundTrip(retry)
}

// keyringSecret sends the request with the keyring secret the server
// references, in the header the secret describes.
func (t *credentialTransport) keyringSecret(req *http.Request) (*http.Response, error) {
	t.kr.mu.Lock()
	h := t.kr.header
	if h == nil {
		got, err := t.c.keyring.SecretHeader(req.Context(), t.cred.KeyringRef(), t.use())
		if err != nil {
			t.kr.mu.Unlock()
			return nil, t.keyringErr(err)
		}
		h = &got
		t.kr.header = h
	}
	t.kr.mu.Unlock()
	req = cloneWithBody(req)
	// The secret chose the header, so the audit trail is told which one to mask.
	req = req.WithContext(audit.WithRedactedHeader(req.Context(), h.Name))
	req.Header.Set(h.Name, h.Value)
	return t.next.RoundTrip(req)
}

// keyringErr is a keyring failure as the caller of the server sees it. The
// keyring's errors name the credential and never carry its value.
func (t *credentialTransport) keyringErr(err error) error {
	t.c.log.Warn("upstream: keyring credential unavailable", "slug", t.srv.Slug, "credential", t.cred.KeyringRef().String(), "owner", t.cred.KeyringOwner, "err", err)
	return fmt.Errorf("upstream: keyring credential unavailable: %w", err)
}
