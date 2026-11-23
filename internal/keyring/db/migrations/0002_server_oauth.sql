-- The keyring holds the OAuth connections of MCP servers: accounts whose provider was discovered
-- from the server rather than written by a person. Three things such an account needs that an
-- ordinary one did not have a column for.
--
-- Every statement is additive and idempotent. Existing rows read as they did: a null token_type is
-- Bearer, a null broken_at is a usable account, a null return_to is the keyring's own connect page.

-- The token type the authorization server issued, honoured when the token is sent.
ALTER TABLE vault.oauth_tokens ADD COLUMN IF NOT EXISTS token_type varchar(32);

-- Set when the token endpoint has definitively refused a refresh (invalid_grant and the like). The
-- account is not refreshed again and is not handed out until it is authorized anew.
ALTER TABLE vault.oauth_tokens ADD COLUMN IF NOT EXISTS broken_at timestamptz;
ALTER TABLE vault.oauth_tokens ADD COLUMN IF NOT EXISTS broken_reason varchar(1024);

-- Where the browser goes once a server's consent round trip has finished.
ALTER TABLE vault.oauth_states ADD COLUMN IF NOT EXISTS return_to text;
