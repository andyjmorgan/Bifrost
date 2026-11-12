-- Entitlements: the grantable view of a server, and the only way a server
-- is reached. The slug moves from the server to the entitlement, so one
-- server can be exposed as several slugs (toolsets, read vs write), each
-- cut to its own selection. Grants give users or a role the entitlement.
-- The server keeps a capability ceiling: what any entitlement may expose.
-- The old route-bound shaping policies are dropped; the entitlement is the
-- policy engine from here on.

create table entitlements (
  id          uuid primary key,
  org_id      text not null,
  server_id   uuid not null references servers (id) on delete cascade,
  slug        varchar(24) not null,
  name        text not null,
  description text not null default '',
  selection   jsonb not null default '{"all":true}'::jsonb,
  policies    jsonb not null default '[]'::jsonb,
  generation  int  not null default 1,
  created_at  timestamptz not null default now(),
  updated_at  timestamptz not null default now(),
  unique (org_id, slug)
);
create index entitlements_server_idx on entitlements (server_id);

create table entitlement_grants (
  entitlement_id uuid not null references entitlements (id) on delete cascade,
  subject_type   text not null check (subject_type in ('user','role')),
  subject_id     text not null,
  granted_by     text not null default '',
  granted_at     timestamptz not null default now(),
  primary key (entitlement_id, subject_type, subject_id)
);
create index entitlement_grants_subject_idx on entitlement_grants (subject_type, subject_id);

-- The ceiling. A capability first seen on a server that has already been
-- seeded waits for approval; the first observation after creation (or this
-- migration) approves everything, so a freshly added server just works.
create table server_capabilities (
  server_id     uuid not null references servers (id) on delete cascade,
  kind          text not null check (kind in ('tool','prompt','resource','template')),
  name          text not null,
  fingerprint   text not null default '',
  first_seen_at timestamptz not null default now(),
  last_seen_at  timestamptz not null default now(),
  approved_at   timestamptz,
  primary key (server_id, kind, name)
);

alter table servers add column exclusive      boolean not null default false;
alter table servers add column ceiling_mode   text not null default 'approve' check (ceiling_mode in ('open','approve'));
alter table servers add column ceiling_seeded boolean not null default false;

-- Every server gets a default entitlement under its old slug, held by admins.
insert into entitlements (id, org_id, server_id, slug, name, description)
select gen_random_uuid(), org_id, id, slug, name, 'Everything the server offers' from servers;
insert into entitlement_grants (entitlement_id, subject_type, subject_id)
select id, 'role', 'admin' from entitlements;

alter table servers drop column slug;

drop table if exists policy_bindings;
drop table if exists policies;

-- Audit rows carry the user the actor acted as (a key's creator), so a
-- member can be shown their own events and nothing else.
alter table audit_events add column actor_user_id text not null default '';
create index audit_events_actor_user_idx on audit_events (org_id, actor_user_id, created_at desc);
