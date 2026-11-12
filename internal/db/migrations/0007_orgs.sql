-- Organisations. Until now the gateway was single-tenant: every record was
-- keyed by an owner id that all role-gated humans shared. From here on the
-- tenant key is an org id. Every user gets a personal org at first sign-in;
-- an invite (magic link) moves a user's active org to the inviter's, and
-- leaving moves it back. A user is a member of one or more orgs with a role.

create table orgs (
  id           uuid primary key,
  name         text not null,
  legacy_owner text unique,   -- the pre-org owner id this org was migrated from
  personal_for text unique,   -- subject of the user whose personal org this is
  created_at   timestamptz not null default now()
);

create table users (
  id              text primary key,  -- Keycloak subject (or the dev owner)
  email           text not null default '',
  name            text not null default '',
  personal_org_id uuid not null references orgs (id),
  active_org_id   uuid not null references orgs (id),
  created_at      timestamptz not null default now(),
  last_seen_at    timestamptz not null default now()
);

create table org_members (
  org_id    uuid not null references orgs (id) on delete cascade,
  user_id   text not null references users (id) on delete cascade,
  role      text not null check (role in ('admin','member')),
  joined_at timestamptz not null default now(),
  primary key (org_id, user_id)
);
create index org_members_user_idx on org_members (user_id);

create table org_invites (
  id         uuid primary key,
  org_id     uuid not null references orgs (id) on delete cascade,
  token_hash bytea not null unique,
  role       text not null default 'member' check (role in ('admin','member')),
  created_by text not null,
  created_at timestamptz not null default now(),
  expires_at timestamptz not null,
  used_at    timestamptz,
  used_by    text
);
create index org_invites_org_idx on org_invites (org_id, created_at desc);

-- Every pre-existing owner id becomes an org. Its first member is assigned at
-- sign-in: the configured admin email (or the dev owner) claims it as their
-- personal org.
insert into orgs (id, name, legacy_owner)
select gen_random_uuid(), o, o from (
  select owner_id as o from servers
  union select owner_id from api_keys
  union select owner_id from policies
  union select owner_id from policy_bindings
  union select owner_id from oauth_states
  union select owner_id from audit_events where owner_id <> '-'
) d;

update servers         s set owner_id = o.id::text from orgs o where o.legacy_owner = s.owner_id;
update api_keys        s set owner_id = o.id::text from orgs o where o.legacy_owner = s.owner_id;
update policies        s set owner_id = o.id::text from orgs o where o.legacy_owner = s.owner_id;
update policy_bindings s set owner_id = o.id::text from orgs o where o.legacy_owner = s.owner_id;
update oauth_states    s set owner_id = o.id::text from orgs o where o.legacy_owner = s.owner_id;
update audit_events    s set owner_id = o.id::text from orgs o where o.legacy_owner = s.owner_id;

alter table servers         rename column owner_id to org_id;
alter table api_keys        rename column owner_id to org_id;
alter table policies        rename column owner_id to org_id;
alter table policy_bindings rename column owner_id to org_id;
alter table oauth_states    rename column owner_id to org_id;
alter table audit_events    rename column owner_id to org_id;

-- A key acts as the user who created it, so it inherits their role and dies
-- when they leave the org. Legacy keys are attributed when the org is claimed.
alter table api_keys add column created_by text;
