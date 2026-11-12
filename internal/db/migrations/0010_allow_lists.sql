-- The selection becomes per-kind allow lists: a key present means only
-- what it names, a key absent means everything of that kind. {"all":true}
-- therefore becomes {}.
update entitlements set selection = selection - 'all';
