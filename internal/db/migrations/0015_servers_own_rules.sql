-- A server owns its slug, its selection, its rules and its grants. The
-- separate grantable view of a server is gone: one server is one slug and
-- one view. Each server takes these from the one view it had; a server with
-- more than one cannot be folded without losing one, so the upgrade stops.

do $$
begin
  if exists (select 1 from entitlements group by server_id having count(*) > 1) then
    raise exception 'a server has more than one entitlement; delete all but one before upgrading';
  end if;
end $$;

alter table servers add column slug       varchar(24);
alter table servers add column selection  jsonb not null default '{}'::jsonb;
alter table servers add column hooks      jsonb not null default '{}'::jsonb;
alter table servers add column generation int   not null default 1;

update servers s
   set slug = e.slug, selection = e.selection - 'all', hooks = e.hooks, generation = e.generation
  from entitlements e
 where e.server_id = s.id;

-- A server no view reached keeps working under a slug made from its name and
-- id. It has no grants, so only its settings page shows it.
update servers
   set slug = left(coalesce(nullif(trim(both '-' from regexp_replace(lower(name), '[^a-z0-9]+', '-', 'g')), ''), 'server'), 15)
              || '-' || left(replace(id::text, '-', ''), 8)
 where slug is null;

alter table servers alter column slug set not null;
alter table servers add constraint servers_org_slug_key unique (org_id, slug);

create table server_grants (
  server_id    uuid not null references servers (id) on delete cascade,
  subject_type text not null check (subject_type in ('user','role')),
  subject_id   text not null,
  granted_by   text not null default '',
  granted_at   timestamptz not null default now(),
  primary key (server_id, subject_type, subject_id)
);
create index server_grants_subject_idx on server_grants (subject_type, subject_id);

insert into server_grants (server_id, subject_type, subject_id, granted_by, granted_at)
select e.server_id, g.subject_type, g.subject_id, g.granted_by, g.granted_at
  from entitlement_grants g join entitlements e on e.id = g.entitlement_id;

drop table entitlement_grants;
drop table entitlements;
alter table servers drop column exclusive;
