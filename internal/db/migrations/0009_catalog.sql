-- No approval on the server. What a server has been seen to offer is
-- governance data, shown on the server and used by the entitlement editor;
-- filtering belongs to the entitlement's selection alone.
alter table server_capabilities drop column approved_at;
alter table servers drop column ceiling_mode;
alter table servers drop column ceiling_seeded;
