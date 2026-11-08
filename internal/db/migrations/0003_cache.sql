-- TTL cache in Postgres instead of a separate key-value store.
--
-- Rows carry expires_at (meta and created_at were dropped in 0005).
-- instant they expire (for non-superuser roles; the bootstrap superuser
-- bypasses RLS, so queries also filter explicitly), and a pg_cron job deletes
-- them every minute. Values are the JSON-RPC results themselves (tools/list
-- pages, tool results, prompts, resources); meta holds provenance.
-- pg_cron is optional (local dev runs the plain postgres image): without it
-- the worker's periodic sweep is the only reaper, which is fine for dev.
do $$
begin
  if exists (select 1 from pg_available_extensions where name = 'pg_cron') then
    execute 'create extension if not exists pg_cron';
  else
    raise notice 'pg_cron not available; cache rows are swept by the worker only';
  end if;
end $$;

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
  -- Nested so the cron.job reference is only prepared when the extension exists.
  if exists (select 1 from pg_extension where extname = 'pg_cron') then
    if not exists (select 1 from cron.job where jobname = 'cache-expire') then
      perform cron.schedule('cache-expire', '* * * * *', $job$delete from cache where expires_at <= now()$job$);
    end if;
  end if;
end $$;
