-- Tie cache rows to their server with a real column so a server and its live
-- cache come back from one indexed join, and deleting a server drops its cache.
alter table cache add column if not exists server_id uuid references servers(id) on delete cascade;
update cache set server_id = (meta->>'serverId')::uuid
  where server_id is null and meta->>'serverId' ~ '^[0-9a-f-]{36}$'
    and exists (select 1 from servers s where s.id = (meta->>'serverId')::uuid);
drop index if exists cache_meta_server_idx;
create index if not exists cache_server_ns_idx on cache (server_id, ns, expires_at);
