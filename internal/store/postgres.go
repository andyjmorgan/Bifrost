package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Postgres implements the store against a pgx pool.
type Postgres struct {
	pool *pgxpool.Pool
}

// NewPostgres wraps an open pool.
func NewPostgres(pool *pgxpool.Pool) *Postgres { return &Postgres{pool: pool} }

func isUnique(err error) bool {
	var pg *pgconn.PgError
	return errors.As(err, &pg) && pg.Code == "23505"
}

// ---- servers -----------------------------------------------------------

const serverCols = `id, slug, name, url, description, auth_kind, enabled, selection, hooks, generation, created_at, updated_at`

// serverColsS is serverCols qualified for a query that aliases servers as s.
const serverColsS = `s.id, s.slug, s.name, s.url, s.description, s.auth_kind, s.enabled, s.selection, s.hooks, s.generation, s.created_at, s.updated_at`

func scanServer(row pgx.Row) (*Server, error) { return scanServerInto(row) }

// scanServerInto scans serverCols, then any extra columns that follow them.
func scanServerInto(row pgx.Row, extra ...any) (*Server, error) {
	var s Server
	var sel []byte
	err := row.Scan(append([]any{&s.ID, &s.Slug, &s.Name, &s.URL, &s.Description, &s.AuthKind, &s.Enabled, &sel, &s.Hooks, &s.Generation, &s.CreatedAt, &s.UpdatedAt}, extra...)...)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: %w", err)
	}
	_ = json.Unmarshal(sel, &s.Selection)
	if s.Selection == nil {
		s.Selection = Selection{}
	}
	if len(s.Hooks) == 0 {
		s.Hooks = json.RawMessage("{}")
	}
	return &s, nil
}

// CreateServer inserts a server with its grants. ErrConflict when the slug
// is taken.
func (p *Postgres) CreateServer(ctx context.Context, s *Server, by string) error {
	if s.Selection == nil {
		s.Selection = Selection{}
	}
	sel, _ := json.Marshal(s.Selection)
	if len(s.Hooks) == 0 {
		s.Hooks = json.RawMessage("{}")
	}
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("store: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	err = tx.QueryRow(ctx, `insert into servers (id, slug, name, url, description, auth_kind, enabled, selection, hooks, created_at, updated_at)
		values ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11) returning generation`,
		s.ID, s.Slug, s.Name, s.URL, s.Description, s.AuthKind, s.Enabled, sel, s.Hooks, s.CreatedAt, s.UpdatedAt).Scan(&s.Generation)
	if isUnique(err) {
		return ErrConflict
	}
	if err != nil {
		return fmt.Errorf("store: %w", err)
	}
	if err := insertGrants(ctx, tx, s.ID, s.Grants, by); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("store: %w", err)
	}
	return nil
}

// GetServer fetches one server with its grants.
func (p *Postgres) GetServer(ctx context.Context, id uuid.UUID) (*Server, error) {
	s, err := scanServer(p.pool.QueryRow(ctx, `select `+serverCols+` from servers where id=$1`, id))
	if err != nil {
		return nil, err
	}
	if err := p.withGrants(ctx, s); err != nil {
		return nil, err
	}
	return s, nil
}

// ListServers returns every server with its grants, oldest first.
func (p *Postgres) ListServers(ctx context.Context) ([]*Server, error) {
	rows, err := p.pool.Query(ctx, `select `+serverCols+` from servers order by created_at`)
	if err != nil {
		return nil, fmt.Errorf("store: %w", err)
	}
	defer rows.Close()
	var out []*Server
	for rows.Next() {
		s, err := scanServer(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: %w", err)
	}
	if err := p.withGrants(ctx, out...); err != nil {
		return nil, err
	}
	return out, nil
}

// UpdateServer writes the connection fields (not slug, rules or grants) and
// refreshes s.UpdatedAt, which conditional cache writes are checked against.
// Disabling a server drops its cache rows: nothing should advertise its tools.
func (p *Postgres) UpdateServer(ctx context.Context, s *Server) error {
	err := p.pool.QueryRow(ctx, `update servers set name=$2, url=$3, description=$4, enabled=$5, auth_kind=$6, updated_at=now() where id=$1 returning updated_at`,
		s.ID, s.Name, s.URL, s.Description, s.Enabled, s.AuthKind).Scan(&s.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("store: %w", err)
	}
	if !s.Enabled {
		if _, err := p.pool.Exec(ctx, `delete from cache where server_id=$1`, s.ID); err != nil {
			return fmt.Errorf("store: %w", err)
		}
	}
	return nil
}

// DeleteServer removes a server and, by cascade, its credential.
func (p *Postgres) DeleteServer(ctx context.Context, id uuid.UUID) error {
	tag, err := p.pool.Exec(ctx, `delete from servers where id=$1`, id)
	if err != nil {
		return fmt.Errorf("store: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ---- credentials -------------------------------------------------------

const credCols = `server_id, kind, state, coalesce(state_reason,''), coalesce(basic_username,''), coalesce(header_name,''),
coalesce(oauth_issuer,''), coalesce(oauth_authorization_endpoint,''), coalesce(oauth_token_endpoint,''), coalesce(oauth_registration_endpoint,''),
coalesce(oauth_registration_kind,''), coalesce(oauth_client_id,''), coalesce(oauth_resource,''), oauth_scopes, coalesce(oauth_token_type,''),
oauth_expires_at, oauth_has_refresh_token, secret_blob, last_refreshed_at, coalesce(last_error,''), updated_at,
coalesce(keyring_owner,''), coalesce(keyring_provider,''), coalesce(keyring_account,''), coalesce(keyring_secret_name,'')`

func scanCredential(row pgx.Row) (*Credential, error) {
	var c Credential
	err := row.Scan(&c.ServerID, &c.Kind, &c.State, &c.StateReason, &c.BasicUsername, &c.HeaderName,
		&c.OAuthIssuer, &c.OAuthAuthorizationEndpoint, &c.OAuthTokenEndpoint, &c.OAuthRegistrationEndpoint,
		&c.OAuthRegistrationKind, &c.OAuthClientID, &c.OAuthResource, &c.OAuthScopes, &c.OAuthTokenType,
		&c.OAuthExpiresAt, &c.OAuthHasRefreshToken, &c.SecretBlob, &c.LastRefreshedAt, &c.LastError, &c.UpdatedAt,
		&c.KeyringOwner, &c.KeyringProvider, &c.KeyringAccount, &c.KeyringSecretName)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: %w", err)
	}
	if c.OAuthScopes == nil {
		c.OAuthScopes = []string{}
	}
	return &c, nil
}

// UpsertCredential replaces the credential row for a server.
func (p *Postgres) UpsertCredential(ctx context.Context, c *Credential) error {
	_, err := p.pool.Exec(ctx, `insert into credentials (server_id, kind, state, state_reason, basic_username, header_name,
oauth_issuer, oauth_authorization_endpoint, oauth_token_endpoint, oauth_registration_endpoint, oauth_registration_kind, oauth_client_id,
oauth_resource, oauth_scopes, oauth_token_type, oauth_expires_at, oauth_has_refresh_token, secret_blob, last_refreshed_at, last_error, updated_at,
keyring_owner, keyring_provider, keyring_account, keyring_secret_name)
values ($1,$2,$3,nullif($4,''),nullif($5,''),nullif($6,''),nullif($7,''),nullif($8,''),nullif($9,''),nullif($10,''),nullif($11,''),nullif($12,''),nullif($13,''),$14,nullif($15,''),$16,$17,$18,$19,nullif($20,''),now(),
nullif($21,''),nullif($22,''),nullif($23,''),nullif($24,''))
on conflict (server_id) do update set kind=excluded.kind, state=excluded.state, state_reason=excluded.state_reason,
basic_username=excluded.basic_username, header_name=excluded.header_name, oauth_issuer=excluded.oauth_issuer,
oauth_authorization_endpoint=excluded.oauth_authorization_endpoint, oauth_token_endpoint=excluded.oauth_token_endpoint,
oauth_registration_endpoint=excluded.oauth_registration_endpoint, oauth_registration_kind=excluded.oauth_registration_kind,
oauth_client_id=excluded.oauth_client_id, oauth_resource=excluded.oauth_resource, oauth_scopes=excluded.oauth_scopes,
oauth_token_type=excluded.oauth_token_type, oauth_expires_at=excluded.oauth_expires_at, oauth_has_refresh_token=excluded.oauth_has_refresh_token,
secret_blob=excluded.secret_blob, last_refreshed_at=excluded.last_refreshed_at, last_error=excluded.last_error, updated_at=now(),
keyring_owner=excluded.keyring_owner, keyring_provider=excluded.keyring_provider, keyring_account=excluded.keyring_account,
keyring_secret_name=excluded.keyring_secret_name`,
		c.ServerID, c.Kind, c.State, c.StateReason, c.BasicUsername, c.HeaderName,
		c.OAuthIssuer, c.OAuthAuthorizationEndpoint, c.OAuthTokenEndpoint, c.OAuthRegistrationEndpoint, c.OAuthRegistrationKind, c.OAuthClientID,
		c.OAuthResource, c.OAuthScopes, c.OAuthTokenType, c.OAuthExpiresAt, c.OAuthHasRefreshToken, c.SecretBlob, c.LastRefreshedAt, c.LastError,
		c.KeyringOwner, c.KeyringProvider, c.KeyringAccount, c.KeyringSecretName)
	if err != nil {
		return fmt.Errorf("store: %w", err)
	}
	return p.credentialChanged(ctx, c.ServerID, c.State)
}

// credentialChanged is the bookkeeping after a credential write: the server
// row's updated_at moves so in-flight cache writes from the old credential
// are rejected, and a credential that is not usable (pending consent, broken)
// takes the server's cache rows with it, so nothing advertises tools that
// cannot be called.
func (p *Postgres) credentialChanged(ctx context.Context, serverID uuid.UUID, state CredentialState) error {
	if _, err := p.pool.Exec(ctx, `update servers set updated_at=now() where id=$1`, serverID); err != nil {
		return fmt.Errorf("store: %w", err)
	}
	if state != StateOK {
		if _, err := p.pool.Exec(ctx, `delete from cache where server_id=$1`, serverID); err != nil {
			return fmt.Errorf("store: %w", err)
		}
	}
	return nil
}

// GetCredential fetches a server's credential.
func (p *Postgres) GetCredential(ctx context.Context, serverID uuid.UUID) (*Credential, error) {
	return scanCredential(p.pool.QueryRow(ctx, `select `+credCols+` from credentials where server_id=$1`, serverID))
}

// UpdateOAuthTokens stores a refreshed token set and clears any error.
func (p *Postgres) UpdateOAuthTokens(ctx context.Context, serverID uuid.UUID, blob []byte, tokenType string, expiresAt *time.Time, hasRefresh bool, scopes []string) error {
	_, err := p.pool.Exec(ctx, `update credentials set secret_blob=$2, oauth_token_type=nullif($3,''), oauth_expires_at=$4, oauth_has_refresh_token=$5,
oauth_scopes=case when cardinality($6::text[])>0 then $6 else oauth_scopes end, state='ok', state_reason=null, last_error=null, last_refreshed_at=now(), updated_at=now() where server_id=$1`,
		serverID, blob, tokenType, expiresAt, hasRefresh, scopes)
	if err != nil {
		return fmt.Errorf("store: %w", err)
	}
	return nil
}

// SetCredentialState marks a credential ok/pending/broken with a reason.
func (p *Postgres) SetCredentialState(ctx context.Context, serverID uuid.UUID, state CredentialState, reason string) error {
	_, err := p.pool.Exec(ctx, `update credentials set state=$2, state_reason=nullif($3,''), last_error=case when $2='broken' then nullif($3,'') else last_error end, updated_at=now() where server_id=$1`,
		serverID, state, reason)
	if err != nil {
		return fmt.Errorf("store: %w", err)
	}
	return p.credentialChanged(ctx, serverID, state)
}

// ExpiringOAuthCredentials lists OAuth credentials with a refresh token that
// expire before the deadline (or have no known expiry) and are not broken.
func (p *Postgres) ExpiringOAuthCredentials(ctx context.Context, before time.Time) ([]*Credential, error) {
	rows, err := p.pool.Query(ctx, `select `+credCols+` from credentials where kind='oauth' and state <> 'broken' and oauth_has_refresh_token and (oauth_expires_at is null or oauth_expires_at < $1)`, before)
	if err != nil {
		return nil, fmt.Errorf("store: %w", err)
	}
	defer rows.Close()
	var out []*Credential
	for rows.Next() {
		c, err := scanCredential(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ---- oauth states ------------------------------------------------------

// PutOAuthState stores a pending authorization round trip.
func (p *Postgres) PutOAuthState(ctx context.Context, s *OAuthState) error {
	_, err := p.pool.Exec(ctx, `insert into oauth_states (state, server_id, code_verifier, return_to, expires_at) values ($1,$2,$3,$4,$5)`,
		s.State, s.ServerID, s.CodeVerifier, s.ReturnTo, s.ExpiresAt)
	if err != nil {
		return fmt.Errorf("store: %w", err)
	}
	return nil
}

// TakeOAuthState consumes a state, returning ErrNotFound if unknown or expired.
func (p *Postgres) TakeOAuthState(ctx context.Context, state string) (*OAuthState, error) {
	var s OAuthState
	err := p.pool.QueryRow(ctx, `delete from oauth_states where state=$1 returning state, server_id, code_verifier, return_to, expires_at`, state).
		Scan(&s.State, &s.ServerID, &s.CodeVerifier, &s.ReturnTo, &s.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: %w", err)
	}
	if time.Now().After(s.ExpiresAt) {
		return nil, ErrNotFound
	}
	return &s, nil
}

// ---- api keys ----------------------------------------------------------

const keyCols = `id, name, prefix, secret_hash, secret_blob, scopes, created_at, last_used_at, revoked_at, coalesce(created_by,'')`

func scanKey(row pgx.Row) (*APIKey, error) {
	var k APIKey
	err := row.Scan(&k.ID, &k.Name, &k.Prefix, &k.SecretHash, &k.SecretBlob, &k.Scopes, &k.CreatedAt, &k.LastUsedAt, &k.RevokedAt, &k.CreatedBy)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: %w", err)
	}
	return &k, nil
}

// CreateAPIKey inserts a key.
func (p *Postgres) CreateAPIKey(ctx context.Context, k *APIKey) error {
	_, err := p.pool.Exec(ctx, `insert into api_keys (id, name, prefix, secret_hash, secret_blob, scopes, created_at, last_used_at, revoked_at, created_by) values ($1,$2,$3,$4,$5,$6,$7,$8,$9,nullif($10,''))`,
		k.ID, k.Name, k.Prefix, k.SecretHash, k.SecretBlob, k.Scopes, k.CreatedAt, k.LastUsedAt, k.RevokedAt, k.CreatedBy)
	if err != nil {
		return fmt.Errorf("store: %w", err)
	}
	return nil
}

// GetAPIKeyByHash finds a live key by its secret hash and bumps last_used_at.
func (p *Postgres) GetAPIKeyByHash(ctx context.Context, hash []byte) (*APIKey, error) {
	k, err := scanKey(p.pool.QueryRow(ctx, `update api_keys set last_used_at=now() where secret_hash=$1 and revoked_at is null returning `+keyCols, hash))
	if err != nil {
		return nil, err
	}
	return k, nil
}

// GetAPIKey fetches one key.
func (p *Postgres) GetAPIKey(ctx context.Context, id uuid.UUID) (*APIKey, error) {
	return scanKey(p.pool.QueryRow(ctx, `select `+keyCols+` from api_keys where id=$1`, id))
}

// ListAPIKeys returns every key, newest first.
func (p *Postgres) ListAPIKeys(ctx context.Context) ([]*APIKey, error) {
	rows, err := p.pool.Query(ctx, `select `+keyCols+` from api_keys order by created_at desc`)
	if err != nil {
		return nil, fmt.Errorf("store: %w", err)
	}
	defer rows.Close()
	var out []*APIKey
	for rows.Next() {
		k, err := scanKey(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

// RevokeAPIKey soft-deletes a key.
func (p *Postgres) RevokeAPIKey(ctx context.Context, id uuid.UUID) error {
	tag, err := p.pool.Exec(ctx, `update api_keys set revoked_at=now() where id=$1 and revoked_at is null`, id)
	if err != nil {
		return fmt.Errorf("store: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ---- audit -------------------------------------------------------------

const auditCols = `id, created_at, hop, parent_id, actor_type, actor_subject, api_key_id, coalesce(api_key_prefix,''), server_id, coalesce(slug,''), route,
http_method, path, coalesce(mcp_method,''), coalesce(tool_name,''), coalesce(jsonrpc_id,''), coalesce(protocol_version,''), status_code, outcome,
request_bytes, response_bytes, duration_ms, request_id, coalesce(error_type,''), has_payload, coalesce(actor_user_id,''), rule_hits`

func scanAudit(row pgx.Row) (*AuditEvent, error) {
	var e AuditEvent
	var rules []byte
	err := row.Scan(&e.ID, &e.CreatedAt, &e.Hop, &e.ParentID, &e.ActorType, &e.ActorSubject, &e.APIKeyID, &e.APIKeyPrefix, &e.ServerID, &e.Slug, &e.Route,
		&e.HTTPMethod, &e.Path, &e.McpMethod, &e.ToolName, &e.JSONRPCID, &e.ProtocolVersion, &e.StatusCode, &e.Outcome,
		&e.RequestBytes, &e.ResponseBytes, &e.DurationMS, &e.RequestID, &e.ErrorType, &e.HasPayload, &e.ActorUserID, &rules)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: %w", err)
	}
	if len(rules) > 0 {
		var rh RuleHits
		if json.Unmarshal(rules, &rh) == nil && len(rh.Hits) > 0 {
			e.Rules = &rh
		}
	}
	return &e, nil
}

// InsertAuditEvent writes an event and, when given, its payload.
func (p *Postgres) InsertAuditEvent(ctx context.Context, e *AuditEvent, payload *AuditPayload) error {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("store: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var rules []byte // null when no rule applied
	if e.Rules != nil && len(e.Rules.Hits) > 0 {
		rules, _ = json.Marshal(e.Rules)
	}
	_, err = tx.Exec(ctx, `insert into audit_events (id, created_at, hop, parent_id, actor_type, actor_subject, api_key_id, api_key_prefix, server_id, slug, route,
http_method, path, mcp_method, tool_name, jsonrpc_id, protocol_version, status_code, outcome, request_bytes, response_bytes, duration_ms, request_id, error_type, has_payload, actor_user_id, rule_hits)
values ($1,$2,$3,$4,$5,$6,$7,nullif($8,''),$9,nullif($10,''),$11,$12,$13,nullif($14,''),nullif($15,''),nullif($16,''),nullif($17,''),$18,$19,$20,$21,$22,$23,nullif($24,''),$25,$26,$27)`,
		e.ID, e.CreatedAt, e.Hop, e.ParentID, e.ActorType, e.ActorSubject, e.APIKeyID, e.APIKeyPrefix, e.ServerID, e.Slug, e.Route,
		e.HTTPMethod, e.Path, e.McpMethod, e.ToolName, e.JSONRPCID, e.ProtocolVersion, e.StatusCode, e.Outcome, e.RequestBytes, e.ResponseBytes, e.DurationMS, e.RequestID, e.ErrorType, payload != nil, e.ActorUserID, rules)
	if err != nil {
		return fmt.Errorf("store: %w", err)
	}
	if payload != nil {
		rh, _ := json.Marshal(payload.RequestHeaders)
		sh, _ := json.Marshal(payload.ResponseHeaders)
		if _, err := tx.Exec(ctx, `insert into audit_payloads (event_id, request_headers, request_body, response_headers, response_body) values ($1,$2,$3,$4,$5)`,
			e.ID, rh, payload.RequestBody, sh, payload.ResponseBody); err != nil {
			return fmt.Errorf("store: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("store: %w", err)
	}
	return nil
}

// ListAuditEvents pages through events, newest first.
func (p *Postgres) ListAuditEvents(ctx context.Context, q AuditQuery) (*Page[*AuditEvent], error) {
	where := []string{"true"}
	var args []any
	add := func(cond string, v any) {
		args = append(args, v)
		where = append(where, fmt.Sprintf(cond, len(args)))
	}
	if q.ServerID != nil {
		add("server_id=$%d", *q.ServerID)
	}
	if q.Slug != "" {
		add("slug=$%d", q.Slug)
	}
	if q.Hop != "" {
		add("hop=$%d", q.Hop)
	}
	if q.McpMethod != "" {
		add("mcp_method=$%d", q.McpMethod)
	}
	if q.ToolName != "" {
		add("tool_name=$%d", q.ToolName)
	}
	if q.Outcome != "" {
		add("outcome=$%d", q.Outcome)
	}
	if q.RequestID != "" {
		add("request_id=$%d", q.RequestID)
	}
	w := strings.Join(where, " and ")
	var total int
	if err := p.pool.QueryRow(ctx, `select count(*) from audit_events where `+w, args...).Scan(&total); err != nil {
		return nil, fmt.Errorf("store: %w", err)
	}
	limit := q.Limit
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	args = append(args, limit, max(q.Offset, 0))
	rows, err := p.pool.Query(ctx, fmt.Sprintf(`select %s from audit_events where %s order by created_at desc, hop asc limit $%d offset $%d`, auditCols, w, len(args)-1, len(args)), args...)
	if err != nil {
		return nil, fmt.Errorf("store: %w", err)
	}
	defer rows.Close()
	page := &Page[*AuditEvent]{Items: []*AuditEvent{}, Total: total, Offset: max(q.Offset, 0), Limit: limit}
	for rows.Next() {
		e, err := scanAudit(rows)
		if err != nil {
			return nil, err
		}
		page.Items = append(page.Items, e)
	}
	return page, rows.Err()
}

// GetAuditEvent fetches an event and its payload, if stored.
func (p *Postgres) GetAuditEvent(ctx context.Context, id uuid.UUID) (*AuditEvent, *AuditPayload, error) {
	e, err := scanAudit(p.pool.QueryRow(ctx, `select `+auditCols+` from audit_events where id=$1`, id))
	if err != nil {
		return nil, nil, err
	}
	if !e.HasPayload {
		return e, nil, nil
	}
	var pl AuditPayload
	var rh, sh []byte
	err = p.pool.QueryRow(ctx, `select event_id, request_headers, request_body, response_headers, response_body from audit_payloads where event_id=$1`, id).
		Scan(&pl.EventID, &rh, &pl.RequestBody, &sh, &pl.ResponseBody)
	if errors.Is(err, pgx.ErrNoRows) {
		return e, nil, nil
	}
	if err != nil {
		return nil, nil, fmt.Errorf("store: %w", err)
	}
	_ = json.Unmarshal(rh, &pl.RequestHeaders)
	_ = json.Unmarshal(sh, &pl.ResponseHeaders)
	return e, &pl, nil
}

// GetAuditTrace returns the inbound root of the exchange that id belongs to
// (nil when the event has no inbound parent, as for API-initiated pings) and
// every hop under it in start order. Without a parent, hops sharing the
// request id form the trace.
func (p *Postgres) GetAuditTrace(ctx context.Context, id uuid.UUID) (*AuditEvent, []*AuditEvent, error) {
	e, err := scanAudit(p.pool.QueryRow(ctx, `select `+auditCols+` from audit_events where id=$1`, id))
	if err != nil {
		return nil, nil, err
	}
	var root *AuditEvent
	switch {
	case e.Hop == HopInbound:
		root = e
	case e.ParentID != nil:
		root, err = scanAudit(p.pool.QueryRow(ctx, `select `+auditCols+` from audit_events where id=$1`, *e.ParentID))
		if err != nil && !errors.Is(err, ErrNotFound) {
			return nil, nil, err
		}
	}
	var rows pgx.Rows
	if root != nil {
		rows, err = p.pool.Query(ctx, `select `+auditCols+` from audit_events where parent_id=$1 order by created_at`, root.ID)
	} else if e.RequestID != "" {
		rows, err = p.pool.Query(ctx, `select `+auditCols+` from audit_events where request_id=$1 and hop='upstream' order by created_at`, e.RequestID)
	} else {
		return nil, []*AuditEvent{e}, nil
	}
	if err != nil {
		return nil, nil, fmt.Errorf("store: %w", err)
	}
	defer rows.Close()
	var spans []*AuditEvent
	for rows.Next() {
		s, err := scanAudit(rows)
		if err != nil {
			return nil, nil, err
		}
		spans = append(spans, s)
	}
	if spans == nil {
		spans = []*AuditEvent{}
	}
	return root, spans, rows.Err()
}

// PurgeAuditBefore deletes events (and payloads by cascade) older than t.
func (p *Postgres) PurgeAuditBefore(ctx context.Context, t time.Time) (int64, error) {
	tag, err := p.pool.Exec(ctx, `delete from audit_events where created_at < $1`, t)
	if err != nil {
		return 0, fmt.Errorf("store: %w", err)
	}
	return tag.RowsAffected(), nil
}

// PurgeExpiredOAuthStates removes stale consent round trips.
func (p *Postgres) PurgeExpiredOAuthStates(ctx context.Context) error {
	_, err := p.pool.Exec(ctx, `delete from oauth_states where expires_at < now()`)
	if err != nil {
		return fmt.Errorf("store: %w", err)
	}
	return nil
}
