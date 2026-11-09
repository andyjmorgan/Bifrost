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

const serverCols = `id, owner_id, slug, name, url, description, auth_kind, enabled, created_at, updated_at`

func scanServer(row pgx.Row) (*Server, error) {
	var s Server
	err := row.Scan(&s.ID, &s.OwnerID, &s.Slug, &s.Name, &s.URL, &s.Description, &s.AuthKind, &s.Enabled, &s.CreatedAt, &s.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: %w", err)
	}
	return &s, nil
}

// CreateServer inserts a server with the given slug; ErrConflict on a slug clash.
func (p *Postgres) CreateServer(ctx context.Context, s *Server) error {
	_, err := p.pool.Exec(ctx, `insert into servers (`+serverCols+`) values ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
		s.ID, s.OwnerID, s.Slug, s.Name, s.URL, s.Description, s.AuthKind, s.Enabled, s.CreatedAt, s.UpdatedAt)
	if isUnique(err) {
		return ErrConflict
	}
	if err != nil {
		return fmt.Errorf("store: %w", err)
	}
	return nil
}

// SlugTaken reports whether owner already uses slug.
func (p *Postgres) SlugTaken(ctx context.Context, owner, slug string) (bool, error) {
	var taken bool
	if err := p.pool.QueryRow(ctx, `select exists(select 1 from servers where owner_id=$1 and slug=$2)`, owner, slug).Scan(&taken); err != nil {
		return false, fmt.Errorf("store: %w", err)
	}
	return taken, nil
}

// GetServer fetches by id within an owner.
func (p *Postgres) GetServer(ctx context.Context, owner string, id uuid.UUID) (*Server, error) {
	return scanServer(p.pool.QueryRow(ctx, `select `+serverCols+` from servers where owner_id=$1 and id=$2`, owner, id))
}

// GetServerBySlug fetches by slug within an owner.
func (p *Postgres) GetServerBySlug(ctx context.Context, owner, slug string) (*Server, error) {
	return scanServer(p.pool.QueryRow(ctx, `select `+serverCols+` from servers where owner_id=$1 and slug=$2`, owner, slug))
}

// ListServers returns an owner's servers, oldest first.
func (p *Postgres) ListServers(ctx context.Context, owner string) ([]*Server, error) {
	rows, err := p.pool.Query(ctx, `select `+serverCols+` from servers where owner_id=$1 order by created_at`, owner)
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
	return out, rows.Err()
}

// UpdateServer writes the mutable fields (not slug, owner, auth kind) and
// refreshes s.UpdatedAt, which conditional cache writes are checked against.
// Disabling a server drops its cache rows: nothing should advertise its tools.
func (p *Postgres) UpdateServer(ctx context.Context, s *Server) error {
	err := p.pool.QueryRow(ctx, `update servers set name=$3, url=$4, description=$5, enabled=$6, auth_kind=$7, updated_at=now() where owner_id=$1 and id=$2 returning updated_at`,
		s.OwnerID, s.ID, s.Name, s.URL, s.Description, s.Enabled, s.AuthKind).Scan(&s.UpdatedAt)
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
func (p *Postgres) DeleteServer(ctx context.Context, owner string, id uuid.UUID) error {
	tag, err := p.pool.Exec(ctx, `delete from servers where owner_id=$1 and id=$2`, owner, id)
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
oauth_expires_at, oauth_has_refresh_token, secret_blob, last_refreshed_at, coalesce(last_error,''), updated_at`

func scanCredential(row pgx.Row) (*Credential, error) {
	var c Credential
	err := row.Scan(&c.ServerID, &c.Kind, &c.State, &c.StateReason, &c.BasicUsername, &c.HeaderName,
		&c.OAuthIssuer, &c.OAuthAuthorizationEndpoint, &c.OAuthTokenEndpoint, &c.OAuthRegistrationEndpoint,
		&c.OAuthRegistrationKind, &c.OAuthClientID, &c.OAuthResource, &c.OAuthScopes, &c.OAuthTokenType,
		&c.OAuthExpiresAt, &c.OAuthHasRefreshToken, &c.SecretBlob, &c.LastRefreshedAt, &c.LastError, &c.UpdatedAt)
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
oauth_resource, oauth_scopes, oauth_token_type, oauth_expires_at, oauth_has_refresh_token, secret_blob, last_refreshed_at, last_error, updated_at)
values ($1,$2,$3,nullif($4,''),nullif($5,''),nullif($6,''),nullif($7,''),nullif($8,''),nullif($9,''),nullif($10,''),nullif($11,''),nullif($12,''),nullif($13,''),$14,nullif($15,''),$16,$17,$18,$19,nullif($20,''),now())
on conflict (server_id) do update set kind=excluded.kind, state=excluded.state, state_reason=excluded.state_reason,
basic_username=excluded.basic_username, header_name=excluded.header_name, oauth_issuer=excluded.oauth_issuer,
oauth_authorization_endpoint=excluded.oauth_authorization_endpoint, oauth_token_endpoint=excluded.oauth_token_endpoint,
oauth_registration_endpoint=excluded.oauth_registration_endpoint, oauth_registration_kind=excluded.oauth_registration_kind,
oauth_client_id=excluded.oauth_client_id, oauth_resource=excluded.oauth_resource, oauth_scopes=excluded.oauth_scopes,
oauth_token_type=excluded.oauth_token_type, oauth_expires_at=excluded.oauth_expires_at, oauth_has_refresh_token=excluded.oauth_has_refresh_token,
secret_blob=excluded.secret_blob, last_refreshed_at=excluded.last_refreshed_at, last_error=excluded.last_error, updated_at=now()`,
		c.ServerID, c.Kind, c.State, c.StateReason, c.BasicUsername, c.HeaderName,
		c.OAuthIssuer, c.OAuthAuthorizationEndpoint, c.OAuthTokenEndpoint, c.OAuthRegistrationEndpoint, c.OAuthRegistrationKind, c.OAuthClientID,
		c.OAuthResource, c.OAuthScopes, c.OAuthTokenType, c.OAuthExpiresAt, c.OAuthHasRefreshToken, c.SecretBlob, c.LastRefreshedAt, c.LastError)
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

// ServerOwner returns the owner of a server id (for worker paths without a caller).
func (p *Postgres) ServerOwner(ctx context.Context, id uuid.UUID) (*Server, error) {
	return scanServer(p.pool.QueryRow(ctx, `select `+serverCols+` from servers where id=$1`, id))
}

// ---- oauth states ------------------------------------------------------

// PutOAuthState stores a pending authorization round trip.
func (p *Postgres) PutOAuthState(ctx context.Context, s *OAuthState) error {
	_, err := p.pool.Exec(ctx, `insert into oauth_states (state, owner_id, server_id, code_verifier, return_to, expires_at) values ($1,$2,$3,$4,$5,$6)`,
		s.State, s.OwnerID, s.ServerID, s.CodeVerifier, s.ReturnTo, s.ExpiresAt)
	if err != nil {
		return fmt.Errorf("store: %w", err)
	}
	return nil
}

// TakeOAuthState consumes a state, returning ErrNotFound if unknown or expired.
func (p *Postgres) TakeOAuthState(ctx context.Context, state string) (*OAuthState, error) {
	var s OAuthState
	err := p.pool.QueryRow(ctx, `delete from oauth_states where state=$1 returning state, owner_id, server_id, code_verifier, return_to, expires_at`, state).
		Scan(&s.State, &s.OwnerID, &s.ServerID, &s.CodeVerifier, &s.ReturnTo, &s.ExpiresAt)
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

const keyCols = `id, owner_id, name, prefix, secret_hash, secret_blob, scopes, created_at, last_used_at, revoked_at`

func scanKey(row pgx.Row) (*APIKey, error) {
	var k APIKey
	err := row.Scan(&k.ID, &k.OwnerID, &k.Name, &k.Prefix, &k.SecretHash, &k.SecretBlob, &k.Scopes, &k.CreatedAt, &k.LastUsedAt, &k.RevokedAt)
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
	_, err := p.pool.Exec(ctx, `insert into api_keys (`+keyCols+`) values ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
		k.ID, k.OwnerID, k.Name, k.Prefix, k.SecretHash, k.SecretBlob, k.Scopes, k.CreatedAt, k.LastUsedAt, k.RevokedAt)
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

// GetAPIKey fetches one of an owner's keys.
func (p *Postgres) GetAPIKey(ctx context.Context, owner string, id uuid.UUID) (*APIKey, error) {
	return scanKey(p.pool.QueryRow(ctx, `select `+keyCols+` from api_keys where owner_id=$1 and id=$2`, owner, id))
}

// ListAPIKeys returns an owner's keys, newest first.
func (p *Postgres) ListAPIKeys(ctx context.Context, owner string) ([]*APIKey, error) {
	rows, err := p.pool.Query(ctx, `select `+keyCols+` from api_keys where owner_id=$1 order by created_at desc`, owner)
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
func (p *Postgres) RevokeAPIKey(ctx context.Context, owner string, id uuid.UUID) error {
	tag, err := p.pool.Exec(ctx, `update api_keys set revoked_at=now() where owner_id=$1 and id=$2 and revoked_at is null`, owner, id)
	if err != nil {
		return fmt.Errorf("store: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ---- audit -------------------------------------------------------------

const auditCols = `id, owner_id, created_at, hop, parent_id, actor_type, actor_subject, api_key_id, coalesce(api_key_prefix,''), server_id, coalesce(slug,''), route,
http_method, path, coalesce(mcp_method,''), coalesce(tool_name,''), coalesce(jsonrpc_id,''), coalesce(protocol_version,''), status_code, outcome,
request_bytes, response_bytes, duration_ms, request_id, coalesce(error_type,''), has_payload`

func scanAudit(row pgx.Row) (*AuditEvent, error) {
	var e AuditEvent
	err := row.Scan(&e.ID, &e.OwnerID, &e.CreatedAt, &e.Hop, &e.ParentID, &e.ActorType, &e.ActorSubject, &e.APIKeyID, &e.APIKeyPrefix, &e.ServerID, &e.Slug, &e.Route,
		&e.HTTPMethod, &e.Path, &e.McpMethod, &e.ToolName, &e.JSONRPCID, &e.ProtocolVersion, &e.StatusCode, &e.Outcome,
		&e.RequestBytes, &e.ResponseBytes, &e.DurationMS, &e.RequestID, &e.ErrorType, &e.HasPayload)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: %w", err)
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
	_, err = tx.Exec(ctx, `insert into audit_events (id, owner_id, created_at, hop, parent_id, actor_type, actor_subject, api_key_id, api_key_prefix, server_id, slug, route,
http_method, path, mcp_method, tool_name, jsonrpc_id, protocol_version, status_code, outcome, request_bytes, response_bytes, duration_ms, request_id, error_type, has_payload)
values ($1,$2,$3,$4,$5,$6,$7,$8,nullif($9,''),$10,nullif($11,''),$12,$13,$14,nullif($15,''),nullif($16,''),nullif($17,''),nullif($18,''),$19,$20,$21,$22,$23,$24,nullif($25,''),$26)`,
		e.ID, e.OwnerID, e.CreatedAt, e.Hop, e.ParentID, e.ActorType, e.ActorSubject, e.APIKeyID, e.APIKeyPrefix, e.ServerID, e.Slug, e.Route,
		e.HTTPMethod, e.Path, e.McpMethod, e.ToolName, e.JSONRPCID, e.ProtocolVersion, e.StatusCode, e.Outcome, e.RequestBytes, e.ResponseBytes, e.DurationMS, e.RequestID, e.ErrorType, payload != nil)
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

// ListAuditEvents pages through an owner's events, newest first.
func (p *Postgres) ListAuditEvents(ctx context.Context, q AuditQuery) (*Page[*AuditEvent], error) {
	where := []string{"owner_id=$1"}
	args := []any{q.OwnerID}
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
func (p *Postgres) GetAuditEvent(ctx context.Context, owner string, id uuid.UUID) (*AuditEvent, *AuditPayload, error) {
	e, err := scanAudit(p.pool.QueryRow(ctx, `select `+auditCols+` from audit_events where owner_id=$1 and id=$2`, owner, id))
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
func (p *Postgres) GetAuditTrace(ctx context.Context, owner string, id uuid.UUID) (*AuditEvent, []*AuditEvent, error) {
	e, err := scanAudit(p.pool.QueryRow(ctx, `select `+auditCols+` from audit_events where owner_id=$1 and id=$2`, owner, id))
	if err != nil {
		return nil, nil, err
	}
	var root *AuditEvent
	switch {
	case e.Hop == HopInbound:
		root = e
	case e.ParentID != nil:
		root, err = scanAudit(p.pool.QueryRow(ctx, `select `+auditCols+` from audit_events where owner_id=$1 and id=$2`, owner, *e.ParentID))
		if err != nil && !errors.Is(err, ErrNotFound) {
			return nil, nil, err
		}
	}
	var rows pgx.Rows
	if root != nil {
		rows, err = p.pool.Query(ctx, `select `+auditCols+` from audit_events where owner_id=$1 and parent_id=$2 order by created_at`, owner, root.ID)
	} else if e.RequestID != "" {
		rows, err = p.pool.Query(ctx, `select `+auditCols+` from audit_events where owner_id=$1 and request_id=$2 and hop='upstream' order by created_at`, owner, e.RequestID)
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
