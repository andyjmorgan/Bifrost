-- One tenant, no roles. Organisations, their members and invites go, and
-- so do the admin and member roles. Everyone who signs in can run the
-- gateway; what a person reaches through it is decided by grants alone, one
-- user on one server. A grant to a role becomes a grant to each person who
-- held that role where the server lived, so nobody gains or loses access.
-- Data of more than one organisation cannot be folded into one, so the
-- upgrade stops if more than one holds any.

do $$
begin
  if (select count(distinct org_id) from (
        select org_id from servers
        union all select org_id from api_keys
        union all select org_id from audit_events where org_id <> '-'
      ) t) > 1 then
    raise exception 'more than one organisation has servers, keys or audit events; keep one before upgrading';
  end if;
end $$;

create table server_grants_new (
  server_id  uuid not null references servers (id) on delete cascade,
  user_id    text not null,
  granted_by text not null default '',
  granted_at timestamptz not null default now(),
  primary key (server_id, user_id)
);

insert into server_grants_new (server_id, user_id, granted_by, granted_at)
select server_id, subject_id, granted_by, granted_at from server_grants where subject_type = 'user'
on conflict do nothing;

insert into server_grants_new (server_id, user_id, granted_by, granted_at)
select g.server_id, m.user_id, g.granted_by, g.granted_at
  from server_grants g
  join servers s on s.id = g.server_id
  join org_members m on m.org_id::text = s.org_id and m.role = g.subject_id
 where g.subject_type = 'role'
on conflict do nothing;

drop table server_grants;
alter table server_grants_new rename to server_grants;
alter table server_grants rename constraint server_grants_new_pkey to server_grants_pkey;
alter table server_grants rename constraint server_grants_new_server_id_fkey to server_grants_server_id_fkey;
create index server_grants_user_idx on server_grants (user_id);

alter table servers drop column org_id;
alter table servers add constraint servers_slug_key unique (slug);
create index servers_created_idx on servers (created_at);

alter table api_keys drop column org_id;
create index api_keys_created_idx on api_keys (created_at);

alter table oauth_states drop column org_id;

alter table audit_events drop column org_id;
create index audit_events_actor_user_idx on audit_events (actor_user_id, created_at desc);

alter table users drop column personal_org_id;
alter table users drop column active_org_id;

drop table org_invites;
drop table org_members;
drop table orgs;
