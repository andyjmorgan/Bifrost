-- Keep the cache to what is read: server_id, ns, key, value, expires_at.
alter table cache drop column if exists meta;
alter table cache drop column if exists created_at;
