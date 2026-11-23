-- A server may authenticate upstream with a credential that lives in the
-- keyring instead of one the gateway stores: a connected OAuth account
-- (keyring_oauth) or a stored secret (keyring_secret). The gateway keeps a
-- reference only, and no secret material for these kinds.
--
-- Keyring rows belong to a user, so the reference names its owner: the
-- person who pointed the server at the credential. Lookups stay in that
-- user's keyring.
--
-- Existing rows are untouched: the new columns are null for every other
-- kind, and the kind checks only grow.

alter table servers drop constraint if exists servers_auth_kind_check;
alter table servers add constraint servers_auth_kind_check
  check (auth_kind in ('none','oauth','basic','header','keyring_oauth','keyring_secret'));

alter table credentials drop constraint if exists credentials_kind_check;
alter table credentials add constraint credentials_kind_check
  check (kind in ('none','oauth','basic','header','keyring_oauth','keyring_secret'));

alter table credentials
  add column keyring_owner       text,
  add column keyring_provider    text,
  add column keyring_account     text,
  add column keyring_secret_name text;

-- A keyring credential always says whose keyring and which entry.
alter table credentials add constraint credentials_keyring_ref_check check (
  case kind
    when 'keyring_oauth'  then keyring_owner is not null and keyring_provider is not null and keyring_account is not null
    when 'keyring_secret' then keyring_owner is not null and keyring_secret_name is not null
    else true
  end
);
