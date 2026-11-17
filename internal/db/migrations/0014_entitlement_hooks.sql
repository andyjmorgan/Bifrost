-- The column is the entitlement's hooks: one property per hook, holding the
-- text of that hook's rules. The earlier policies column held the same rules
-- in two retired forms and is no longer read.
alter table entitlements rename column rules to hooks;
alter table entitlements drop column policies;

-- A rule hit names the hook the rule ran on.
update audit_events set rule_hits = replace(rule_hits::text, '"stage":', '"hook":')::jsonb where rule_hits is not null;
