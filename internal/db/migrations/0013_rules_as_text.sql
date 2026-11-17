-- An entitlement's rules are kept as text, one property per hook, in a new
-- column: {"toolsCallPre": "when ...", ...}. A property that is absent means
-- the hook has no rules. The gateway compiles the text when it loads the
-- entitlement; the compiled copy is no longer stored.
--
-- The old policies column is left in place, unread, so nothing is lost by
-- this migration. It can be dropped once the new column has proved itself.
alter table entitlements add column rules jsonb not null default '{}'::jsonb;

-- Rules saved as a bare JSON list have no text to carry over, and text under
-- a retired hook name has nowhere to go. Stop rather than start with those
-- entitlements exposing everything.
do $$
begin
  -- A case, not an and: the planner may test either side first, and
  -- jsonb_array_length fails on anything that is not an array.
  if exists (select 1 from entitlements where
       case jsonb_typeof(policies)
         when 'array' then jsonb_array_length(policies) > 0
         when 'object' then
           (case when jsonb_typeof(policies->'rules') = 'array' then jsonb_array_length(policies->'rules') > 0 else false end)
           and coalesce(policies->'text', '{}'::jsonb) = '{}'::jsonb
         else false
       end) then
    raise exception 'an entitlement has rules stored without their text; rewrite them as text before upgrading';
  end if;
  -- Text under a name that is no longer a hook cannot be carried either.
  if exists (select 1 from entitlements e, jsonb_each_text(case when jsonb_typeof(e.policies->'text') = 'object' then e.policies->'text' else '{}'::jsonb end) t
             where btrim(t.value) <> '' and t.key not in ('prompts/get.post.elicit', 'prompts/get.post.progress', 'prompts/get.pre', 'prompts/list.post', 'resources/list.post', 'resources/read.post.elicit', 'resources/read.post.progress', 'resources/read.post.result', 'resources/read.pre', 'resources/templates/list.post', 'server.session', 'server.settings', 'server/discover.post', 'skills/list.post', 'tools/call.post.elicit', 'tools/call.post.progress', 'tools/call.post.result', 'tools/call.pre', 'tools/list.post')) then
    raise exception 'an entitlement has rules under a hook name that no longer exists; move them before upgrading';
  end if;
end $$;

update entitlements e set rules = coalesce((
  select jsonb_object_agg(m.prop, t.value)
  from jsonb_each_text(e.policies->'text') t
  join (values
    ('prompts/get.post.elicit', 'promptsGetPostElicit'),
    ('prompts/get.post.progress', 'promptsGetPostProgress'),
    ('prompts/get.pre', 'promptsGetPre'),
    ('prompts/list.post', 'promptsListPost'),
    ('resources/list.post', 'resourcesListPost'),
    ('resources/read.post.elicit', 'resourcesReadPostElicit'),
    ('resources/read.post.progress', 'resourcesReadPostProgress'),
    ('resources/read.post.result', 'resourcesReadPostResult'),
    ('resources/read.pre', 'resourcesReadPre'),
    ('resources/templates/list.post', 'resourcesTemplatesListPost'),
    ('server.session', 'serverSession'),
    ('server.settings', 'serverSettings'),
    ('server/discover.post', 'serverDiscoverPost'),
    ('skills/list.post', 'skillsListPost'),
    ('tools/call.post.elicit', 'toolsCallPostElicit'),
    ('tools/call.post.progress', 'toolsCallPostProgress'),
    ('tools/call.post.result', 'toolsCallPostResult'),
    ('tools/call.pre', 'toolsCallPre'),
    ('tools/list.post', 'toolsListPost')
  ) as m(hook, prop) on m.hook = t.key
  where btrim(t.value) <> ''), '{}'::jsonb)
where jsonb_typeof(e.policies) = 'object' and jsonb_typeof(e.policies->'text') = 'object';
