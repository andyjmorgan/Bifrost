// Package ruletext is the text form of an entitlement's rules.
//
// Rules are written as text, one text per hook. A hook is a place in a
// request where rules run: a method, a side of it (pre, the request; post,
// the reply) and, for a reply, what came back. The hooks are the engine's
// (pkg/hooks) and go by the engine's names. Each rule is a condition and what
// to do:
//
//	when tool.name == 'echo'                       deny "Not today."
//	when tool.name == 'delete_page'                confirm "Delete {tool.name}?"
//	when !has(request.params.arguments.limit)      set request.params.arguments.limit = 50
//
// The text compiles to the engine's rules. This package is a way of writing
// them, the form they are stored in (Hooks), and what an editor shows for
// each hook (Hook).
package ruletext

import (
	"sort"

	"github.com/andyjmorgan/Bifrost/pkg/hooks"
)

// Hook describes one of the engine's hooks to someone writing rules on it.
type Hook struct {
	Name string `json:"name"` // the engine's name for the hook: tools/call.post.result
	// Property is the property the hook's rules are kept under in Hooks:
	// toolsCallPostResult.
	Property string `json:"property"`
	Group    string `json:"group"`           // Server, Tools, Prompts, Resources
	Method   string `json:"method"`          // tools/call; "server" for the two that are not a method
	Side     string `json:"side"`            // pre, post, settings or session
	Event    string `json:"event,omitempty"` // progress, elicit or result
	// Item is what a rule here is about: tool, prompt, resource, template,
	// skill or server.
	Item string `json:"item,omitempty"`
	// Summary says what the hook controls, in one sentence.
	Summary string `json:"summary"`
	// Notes are what an admin needs to know before writing a rule here.
	Notes []string `json:"notes,omitempty"`
	// Actions are the actions a rule here may take: deny, confirm, set.
	Actions []string `json:"actions"`
	// Deny says what deny does at this hook.
	Deny string `json:"deny,omitempty"`
	// Tree is everything a rule here can read, to any depth, with what it
	// may set marked writable. It is the engine's own tree for the hook.
	Tree *hooks.Node `json:"tree"`
	// Sets lists the paths a rule here may set: the tree's writable nodes.
	Sets    []hooks.SetSchema `json:"sets"`
	Helpers []string          `json:"helpers,omitempty"`
	// Example is a full set of rules for the hook, to copy from. Every line
	// of it compiles: a test holds it to that.
	Example string `json:"example"`
}

const listOrder = "Rules run top to bottom. A later rule sees what an earlier one changed, so a deny written after a rename matches the new name. Put deny rules first where you can: a hidden item is not worked on further."

const listNote = "A hidden item cannot be called or read either: every call runs this hook first, for the one item it names."

const optionalNote = "A field the server did not send is absent, not empty. Read it with .? and say what absent means: tool.?title.orValue(''). A plain read of an absent field fails the rule, and a rule that fails denies."

const upstreamNote = "upstream is the item's canonical definition, as the server declares it. It never changes, so a rule that must survive a rename matches on upstream.name."

var hookDefs = []Hook{
	{Name: "server.settings", Group: "Server", Method: "server", Side: "settings",
		Summary: "Switch whole parts of this server on or off.",
		Notes:   []string{"A switch covers every method at once. tasks and serverInfo start off; the rest start on."},
		Example: `# Switch a whole kind off for some callers.
when request.route == 'mux'                       set settings.prompts = false
when request.caller.role != 'admin'               set settings.resources = false

# Drop progress messages, the server's instructions, or its place in search.
when request.caller.type == 'apikey'              set settings.progress = false
when true                                         set settings.instructions = false
when request.flags.search                         set settings.searchRoster = false

# Let the server keep its own name and version on a direct route.
when request.route == 'direct'                    set settings.serverInfo = true
`},
	{Name: "server.session", Group: "Server", Method: "server", Side: "session",
		Summary: "What the gateway tells this server about the client when it opens a session.",
		Notes:   []string{"Nothing about the client is passed on unless a rule here says so."},
		Example: `# Present the real client, or a fixed identity.
when has(request.client.clientInfo)               set session.clientInfo = request.client.clientInfo
when request.route == 'mux'                       set session.clientInfo = {'name': 'bifrost', 'version': '1'}

# Let the server ask the user questions.
when request.caller.role == 'admin'               set session.capabilities.elicitation = true

# Forward the client's _meta (only keys on the gateway's input list pass).
when true                                         set session._meta = true
`},
	{Name: "server/discover.post", Group: "Server", Method: "server/discover", Side: "post", Event: "result",
		Summary: "How this server describes itself to the client.",
		Notes:   []string{"server.upstream is what the server says about itself."},
		Example: `when true                                         set server.title = 'Team wiki'
when true                                         set server.description = 'The team wiki, read only.'
when request.route == 'mux'
  set server.instructions = 'Search before you write. ' + server.upstream.?instructions.orValue('')
`},

	{Name: "tools/list.post", Group: "Tools", Method: "tools/list", Side: "post", Event: "result",
		Summary: "Which tools are listed, and how each one looks.", Deny: "hides the tool", Notes: []string{listNote, listOrder, upstreamNote, optionalNote},
		Example: `# Hide tools.
when tool.name.startsWith('delete_')                              deny
when tool.name == 'purge' && request.caller.role != 'admin'       deny "Admins only."

# Rename and redescribe. The client then calls the tool by its new name.
when tool.upstream.name == 'flip'
  set tool.name = 'toss'
  set tool.description = 'Toss a coin.'

# Shape the parameters: a default, a ceiling, a fixed value, a hidden one.
when tool.upstream.name == 'search'
  set tool.params.limit.default = 20
  set tool.params.limit.maximum = 100
  set tool.params.space.pin = 'ENG'
  set tool.params.debug.hide = true

# An optional field may be absent: read it with .? and say what absent means.
when !tool.?annotations.?readOnlyHint.orValue(false)              set tool.annotations.destructiveHint = true
`},
	{Name: "tools/call.pre", Group: "Tools", Method: "tools/call", Side: "pre",
		Summary: "The call before it is sent: whether it runs, and with what arguments.", Deny: "blocks the call",
		Notes: []string{"Runs after tools/list.post, so tool.name is the listed name and tool.upstream.name is the server's.",
			"request.params.arguments holds what the client sent, under the names the client knows. An argument it did not send is absent."},
		Example: `# Block a call.
when tool.name == 'echo'                                          deny "Not today."
when request.params.arguments.?space.orValue('') == 'HR'          deny "HR is restricted."

# Ask the user first. The call goes ahead once they confirm.
when tool.name.startsWith('delete_')                              confirm "Run {tool.name}?"

# Change the arguments.
when tool.name == 'search' && !has(request.params.arguments.limit)
  set request.params.arguments.limit = 50
when tool.name == 'search' && request.params.arguments.?limit.orValue(0) > 100
  set request.params.arguments.limit = 100

# Give a slow tool longer.
when tool.name == 'export'                                        set timeout = duration('120s')

# Try a rule out: audit records what it would do and blocks nothing.
audit when request.caller.role != 'admin'                         deny "Would block non-admins."
`},
	{Name: "tools/call.post.progress", Group: "Tools", Method: "tools/call", Side: "post", Event: "progress",
		Summary: "Each progress message the server sends while the call runs.", Deny: "drops the message",
		Example: `when progress.params.?message.orValue('').contains('internal')    deny
when has(progress.params.message)
  set progress.params.message = 'Working: ' + progress.params.message
`},
	{Name: "tools/call.post.elicit", Group: "Tools", Method: "tools/call", Side: "post", Event: "elicit",
		Summary: "A question the server asks the user in place of a result.", Deny: "refuses the question and ends the call",
		Notes: []string{"This is the server's own question. A confirm rule in pre is the gateway's question and does not pass through here.", "Only a question (an elicitation) reaches this hook. A request for sampling or roots is refused before any rule runs."},
		Example: `when elicitation.key == 'password'                                deny "The server asked for a password."
when tool.upstream.name == 'create_issue'                         deny "This server may not ask users questions."
when has(elicitation.params.message)
  set elicitation.params.message = 'The server asks: ' + elicitation.params.message
`},
	{Name: "tools/call.post.result", Group: "Tools", Method: "tools/call", Side: "post", Event: "result",
		Summary: "The result before the client sees it.", Deny: "blocks the result",
		Notes: []string{"An error the server returns as a JSON-RPC error passes through untouched: no rule sees it.",
			"response.result.content is a list. Work on it with CEL's list macros: exists, all, filter, map.",
			"response.upstream is the response as the server produced it. It never changes.",
			"request.params.arguments is what the client sent; request.upstream.params.arguments is what the server received, under its own names."},
		Example: `# Block a result.
when size(response.result.content) > 50                           deny "The result is too large."

# Trim fields out of the structured result.
when tool.upstream.name == 'get_user' && has(response.result.structuredContent)
  set response.result.structuredContent = drop(response.result.structuredContent, ['ssn', 'internal.*'])

# Replace every image with a notice.
when response.result.content.exists(c, c.type == 'image')
  set response.result.content = response.result.content.map(c,
      c.type == 'image' ? {'type': 'text', 'text': 'This image was removed by policy.'} : c)

# Keep only the structured result.
when has(response.result.structuredContent)                       set response.result.content = []
`},

	{Name: "prompts/list.post", Group: "Prompts", Method: "prompts/list", Side: "post", Event: "result",
		Summary: "Which prompts are listed, and how each one looks.", Deny: "hides the prompt", Notes: []string{listNote, listOrder, upstreamNote, optionalNote},
		Example: `when prompt.name.startsWith('internal_')                          deny
when prompt.upstream.name == 'confluence_summary'
  set prompt.name = 'summary'
  set prompt.title = 'Wiki summary'
  set prompt.params.space.name = 'area'
when has(prompt.description)
  set prompt.description = regex.replace(prompt.description, '(?i)confluence', 'the wiki')
`},
	{Name: "prompts/get.pre", Group: "Prompts", Method: "prompts/get", Side: "pre",
		Summary: "The prompt request before it is sent.", Deny: "blocks the request",
		Example: `when request.params.arguments.?area.orValue('') == 'HR'           deny "HR prompts are restricted."
when prompt.name == 'broadcast'                                   confirm "Use the prompt {prompt.name}?"
when !has(request.params.arguments.tone)                          set request.params.arguments.tone = 'neutral'
`},
	{Name: "prompts/get.post.progress", Group: "Prompts", Method: "prompts/get", Side: "post", Event: "progress",
		Summary: "Each progress message the server sends while it builds the prompt.", Deny: "drops the message",
		Example: `when has(progress.params.message)
  set progress.params.message = 'Preparing: ' + progress.params.message
`},
	{Name: "prompts/get.post.elicit", Group: "Prompts", Method: "prompts/get", Side: "post", Event: "elicit",
		Summary: "A question the server asks the user in place of the prompt.", Deny: "refuses the question and ends the request",
		Notes: []string{"Only a question (an elicitation) reaches this hook. A request for sampling or roots is refused before any rule runs.", "There is no result hook for prompts yet: the prompt the server returns passes through."},
		Example: `when true                                                         deny "This server may not ask users questions."
`},

	{Name: "resources/list.post", Group: "Resources", Method: "resources/list", Side: "post", Event: "result",
		Summary: "Which resources are listed, and how each one looks.", Deny: "hides the resource",
		Notes: []string{listNote, "A read of a URI that is in no list runs only the deny rules here, with only resource.uri to read.", listOrder, optionalNote},
		Example: `when resource.uri.startsWith('file:///secrets/')                  deny
when resource.uri.endsWith('.log')
  set resource.title = 'Log: ' + resource.?name.orValue(resource.uri)
  set resource.annotations.audience = ['assistant']
when resource.?mimeType.orValue('') == 'text/markdown'
  set resource.description = 'Docs: ' + resource.?name.orValue(resource.uri)
`},
	{Name: "resources/templates/list.post", Group: "Resources", Method: "resources/templates/list", Side: "post", Event: "result",
		Summary: "Which resource templates are listed, and how each one looks.", Deny: "hides the template", Notes: []string{listOrder, optionalNote},
		Example: `when template.uriTemplate.startsWith('file:///private/')          deny
when template.uriTemplate.startsWith('file:///logs/')             set template.description = 'Log files'
`},
	{Name: "resources/read.pre", Group: "Resources", Method: "resources/read", Side: "pre",
		Summary: "The read before it is sent.", Deny: "blocks the read",
		Notes: []string{"Runs after resources/list.post."},
		Example: `when resource.uri.contains('/payroll/')                           deny "Payroll files are restricted."
when resource.uri.endsWith('.key')                                confirm "Read {resource.uri}?"
`},
	{Name: "resources/read.post.progress", Group: "Resources", Method: "resources/read", Side: "post", Event: "progress",
		Summary: "Each progress message the server sends while the read runs.", Deny: "drops the message",
		Example: `when true                                                         deny
`},
	{Name: "resources/read.post.elicit", Group: "Resources", Method: "resources/read", Side: "post", Event: "elicit",
		Summary: "A question the server asks the user in place of the contents.", Deny: "refuses the question and ends the read",
		Notes: []string{"Only a question (an elicitation) reaches this hook. A request for sampling or roots is refused before any rule runs."},
		Example: `when true                                                         deny "This server may not ask users questions."
`},
	{Name: "resources/read.post.result", Group: "Resources", Method: "resources/read", Side: "post", Event: "result",
		Summary: "The contents before the client sees them.", Deny: "blocks the read",
		Notes: []string{"A read can return several entries. response.result.contents is the whole list; work on it with CEL's list macros.",
			"A rule may change or drop an entry. It may not add an entry for a URI the read did not return.",
			"resource is the resource the read named. Each entry of contents has its own uri."},
		Example: `# Refuse a read whose contents hold a private key.
when response.result.contents.exists(c, c.?text.orValue('').contains('BEGIN PRIVATE KEY'))
  deny "The file holds a private key."

# Drop binary entries.
when response.result.contents.exists(c, has(c.blob))
  set response.result.contents = response.result.contents.filter(c, !has(c.blob))
`},
	{Name: "skills/list.post", Group: "Resources", Method: "skills/list", Side: "post", Event: "result",
		Summary: "Which skills are listed.", Deny: "hides the skill",
		Example: `when skill.?name.orValue('').startsWith('internal-')              deny
`},
}

var (
	all    []Hook
	byName = map[string]*Hook{}
)

func init() {
	all = make([]Hook, len(hookDefs))
	copy(all, hookDefs)
	for i := range all {
		s := &all[i]
		h, ok := hooks.Hooks[s.Name]
		if !ok {
			panic("ruletext: " + s.Name + " is not an engine hook")
		}
		s.Property = Property(s.Name)
		if _, ok := fieldOf[s.Property]; !ok {
			panic("ruletext: Hooks has no field for " + s.Name + " (" + s.Property + ")")
		}
		d := h.Schema()
		s.Item, s.Tree, s.Sets = d.Item, d.Tree, d.Sets
		if d.Denies {
			s.Actions = append(s.Actions, "deny")
		}
		if d.Confirms {
			s.Actions = append(s.Actions, "confirm")
		}
		if len(s.Sets) > 0 {
			s.Actions = append(s.Actions, "set")
		}
		for _, name := range d.Helpers {
			if name != "elicit" {
				s.Helpers = append(s.Helpers, name)
			}
		}
		sort.Strings(s.Helpers)
		byName[s.Name] = s
	}
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

// List describes every hook, in the order a request meets them.
func List() []Hook { return all }

// Lookup describes the hook of that name.
func Lookup(name string) (Hook, bool) {
	s, ok := byName[name]
	if !ok {
		return Hook{}, false
	}
	return *s, true
}
