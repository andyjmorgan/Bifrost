-- Display metadata survives the short-lived protocol/tool cache.
alter table servers add column icons jsonb not null default '[]'::jsonb;
