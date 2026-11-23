-- The gateway's own credentials (Basic, header, OAuth) move into the keyring
-- with the one-off command `gateway migrate-credentials`. A row it has moved
-- becomes a keyring reference, and this column remembers what it was, so the
-- command's rollback restores exactly the rows it moved and no others: not
-- a reference somebody pointed at their own keyring, and not a credential
-- that was stored in the keyring from the start.
--
-- Null everywhere until the command is run.

alter table credentials add column migrated_from text
  check (migrated_from in ('oauth','basic','header'));
