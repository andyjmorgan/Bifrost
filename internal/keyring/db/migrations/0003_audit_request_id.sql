-- A credential the gateway fetches for an MCP server is fetched for one forwarded request, and the
-- gateway's own audit trail knows that request by its request id. The keyring's row now carries the
-- same id, so a gateway audit event leads to the keyring rows of the credentials it used, and a
-- keyring row leads back to the request it was used for.
--
-- Null for every row that was not written for a gateway request: the keyring's own API calls, and
-- everything recorded before this.

ALTER TABLE vault.audit_log ADD COLUMN IF NOT EXISTS request_id varchar(64);
CREATE INDEX IF NOT EXISTS ix_audit_request_id ON vault.audit_log (request_id) WHERE request_id IS NOT NULL;
