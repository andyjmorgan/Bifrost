-- TTL cache in Postgres instead of a separate key-value store.
--
-- Rows carry expires_at. A row-level-security policy hides expired rows the
-- instant they expire (for non-superuser roles; the bootstrap superuser
-- bypasses RLS, so queries also filter explicitly), and a pg_cron job deletes
-- them every minute. Values are the JSON-RPC results themselves (tools/list
-- pages, tool results, prompts, resources); meta holds provenance.
create extension if not exists pg_cron;

create table if not exists cache (
  ns         text        not null,
  key        text        not null,
  value      jsonb       not null,
  meta       jsonb       not null default '{}'::jsonb,
  created_at timestamptz not null default now(),
  expires_at timestamptz not null,
  primary key (ns, key)
);
create index if not exists cache_expires_idx on cache (expires_at);
create index if not exists cache_meta_server_idx on cache ((meta->>'serverId'));

alter table cache enable row level security;
alter table cache force row level security;
drop policy if exists cache_live on cache;
create policy cache_live on cache using (expires_at > now());

do $$
begin
  if not exists (select 1 from cron.job where jobname = 'cache-expire') then
    perform cron.schedule('cache-expire', '* * * * *', $job$delete from cache where expires_at <= now()$job$);
  end if;
end $$;
