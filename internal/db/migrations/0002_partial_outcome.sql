-- A mux tools/list that succeeded but skipped one or more servers.
alter table audit_events drop constraint audit_events_outcome_check;
alter table audit_events add constraint audit_events_outcome_check
  check (outcome in ('success','error','denied','partial'));
