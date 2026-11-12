-- The catalog keeps the definition it saw, so the server page can show a
-- tool read-only (name, description, annotations, schema). The
-- entitlement's policies column now holds its ordered rules.
alter table server_capabilities add column definition jsonb;
