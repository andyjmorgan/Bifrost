-- The rules that applied to a request, on its inbound event: which rule, on
-- what, in which mode, and what it did or would have done. Null when none did.
alter table audit_events add column rule_hits jsonb;
