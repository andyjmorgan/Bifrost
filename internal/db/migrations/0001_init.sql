create table if not exists schema_migrations (
  version text primary key,
  applied_at timestamptz not null default now()
);

create table servers (
  id          uuid primary key,
  owner_id    text not null,
  slug        varchar(12) not null,
  name        text not null,
  url         text not null,
  description text not null default '',
  auth_kind   text not null check (auth_kind in ('none','oauth','basic','header')),
  enabled     boolean not null default true,
  created_at  timestamptz not null default now(),
  updated_at  timestamptz not null default now(),
  unique (owner_id, slug)
);
create index servers_owner_idx on servers (owner_id, created_at);

-- One credential per server. Non-secret metadata is in columns; the secret
-- material (password / header value / tokens / client secret) is an
-- AES-GCM envelope of a JSON object, bound to the server id.
create table credentials (
  server_id                   uuid primary key references servers (id) on delete cascade,
  kind                        text not null check (kind in ('none','oauth','basic','header')),
  state                       text not null default 'ok' check (state in ('ok','pending','broken')),
  state_reason                text,
  basic_username              text,
  header_name                 text,
  oauth_issuer                text,
  oauth_authorization_endpoint text,
  oauth_token_endpoint        text,
  oauth_registration_endpoint text,
  oauth_registration_kind     text check (oauth_registration_kind in ('cimd','dcr')),
  oauth_client_id             text,
  oauth_resource              text,
  oauth_scopes                text[] not null default '{}',
  oauth_token_type            text,
  oauth_expires_at            timestamptz,
  oauth_has_refresh_token     boolean not null default false,
  secret_blob                 bytea,
  last_refreshed_at           timestamptz,
  last_error                  text,
  updated_at                  timestamptz not null default now()
);
create index credentials_expiry_idx on credentials (oauth_expires_at) where kind = 'oauth';

create table oauth_states (
  state         text primary key,
  owner_id      text not null,
  server_id     uuid not null references servers (id) on delete cascade,
  code_verifier text not null,
  return_to     text not null default '',
  created_at    timestamptz not null default now(),
  expires_at    timestamptz not null
);

create table api_keys (
  id           uuid primary key,
  owner_id     text not null,
  name         text not null,
  prefix       text not null,
  secret_hash  bytea not null unique,
  secret_blob  bytea not null,
  scopes       text[] not null,
  created_at   timestamptz not null default now(),
  last_used_at timestamptz,
  revoked_at   timestamptz
);
create index api_keys_owner_idx on api_keys (owner_id, created_at);

create table audit_events (
  id               uuid primary key,
  owner_id         text not null,
  created_at       timestamptz not null default now(),
  hop              text not null check (hop in ('inbound','upstream')),
  parent_id        uuid,
  actor_type       text not null,
  actor_subject    text not null default '',
  api_key_id       uuid,
  api_key_prefix   text,
  server_id        uuid,
  slug             text,
  route            text not null,
  http_method      text not null,
  path             text not null,
  mcp_method       text,
  tool_name        text,
  jsonrpc_id       text,
  protocol_version text,
  status_code      int not null,
  outcome          text not null check (outcome in ('success','error','denied')),
  request_bytes    bigint not null default 0,
  response_bytes   bigint not null default 0,
  duration_ms      bigint not null default 0,
  request_id       text not null,
  error_type       text,
  has_payload      boolean not null default false
);
create index audit_events_owner_idx on audit_events (owner_id, created_at desc);
create index audit_events_request_idx on audit_events (request_id);
create index audit_events_created_idx on audit_events (created_at);

create table audit_payloads (
  event_id         uuid primary key references audit_events (id) on delete cascade,
  request_headers  jsonb,
  request_body     text,
  response_headers jsonb,
  response_body    text
);
