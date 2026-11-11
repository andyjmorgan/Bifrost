-- Policies: ordered CEL rules that shape what a route exposes (hide, show,
-- annotate, rename, require confirmation). Bindings attach policies to the
-- global /mcp route (server_id null) or to one server's /{slug}/mcp route,
-- in position order. On the mux route a server's own policies run first,
-- then the global ones.
create table if not exists policies (
  id          uuid primary key,
  owner_id    text not null,
  name        text not null,
  description text not null default '',
  invert      boolean not null default false,
  rules       jsonb not null default '[]'::jsonb,
  created_at  timestamptz not null default now(),
  updated_at  timestamptz not null default now(),
  unique (owner_id, name)
);

create table if not exists policy_bindings (
  owner_id   text not null,
  server_id  uuid references servers(id) on delete cascade,
  policy_id  uuid not null references policies(id) on delete cascade,
  position   int  not null default 0
);
create unique index if not exists policy_bindings_route_idx
  on policy_bindings (owner_id, coalesce(server_id, '00000000-0000-0000-0000-000000000000'::uuid), policy_id);
create index if not exists policy_bindings_owner_idx on policy_bindings (owner_id, server_id, position);
