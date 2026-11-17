// Package ruletext is the text form of an entitlement's rules.
//
// An admin writes rules as text, one editor per stage. A stage is a place
// in a request where rules run: a method, a side of it (pre, the request;
// post, the reply) and, for a reply, what came back. A stage is one of the
// engine's hooks, and its ID is the hook's name. Each rule is a condition
// and what to do:
//
//	when name == 'echo'            deny "Not today."
//	when name == 'delete_page'     confirm "Delete {args.id}?"
//	when !has(args.limit)          set args.limit = 50
//
// The text compiles to the engine's rules (pkg/hooks). This package is a
// way of writing them, and holds what the editor shows for each stage.
package ruletext

import (
	"sort"

	"github.com/andyjmorgan/Bifrost/pkg/hooks"
)

// Stage is one place where rules run: one engine hook.
type Stage struct {
	ID string `json:"id"` // the hook's name: tools/call.post.result
	// Property is the property the hook's rules are kept under in Rules:
	// toolsCallPostResult.
	Property string `json:"property"`
	Group    string `json:"group"`           // Server, Tools, Prompts, Resources
	Method   string `json:"method"`          // tools/call; "server" for the two that are not a method
	Side     string `json:"side"`            // pre, post, settings or session
	Event    string `json:"event,omitempty"` // progress, elicit or result
	// Item is what a rule here is about: tool, prompt, resource, template,
	// skill or server.
	Item string `json:"item,omitempty"`
	// Summary says what the stage controls, in one sentence.
	Summary string `json:"summary"`
	// Notes are what an admin needs to know before writing a rule here.
	Notes []string `json:"notes,omitempty"`
	// Actions are the actions a rule here may take: deny, confirm, set.
	Actions []string `json:"actions"`
	// Deny says what deny does at this stage.
	Deny    string            `json:"deny,omitempty"`
	Reads   []hooks.VarSchema `json:"reads"`
	Sets    []hooks.SetSchema `json:"sets"`
	Helpers []string          `json:"helpers,omitempty"`
	// Example is a full set of rules for the stage, to copy from. Every
	// line of it compiles: a test holds it to that.
	Example string `json:"example"`
}

const listOrder = "Rules run top to bottom. A later rule sees what an earlier one changed, so a deny written after a rename matches the new name. Put deny rules first where you can: a hidden item is not worked on further."

const listNote = "A hidden item cannot be called or read either: every call checks this stage first, for the one item it names."

var stageDefs = []Stage{
	{ID: "server.settings", Group: "Server", Method: "server", Side: "settings",
		Summary: "Switch whole parts of this server on or off.",
		Notes:   []string{"A switch covers every method at once, so it sits beside the stages. tasks and serverInfo start off; the rest start on."},
		Example: `# Switch a whole kind off for some callers.
when route == 'mux'                 set prompts = false
when caller.role != 'admin'         set resources = false

# Drop progress messages, the server's instructions, or its place in search.
when caller.type == 'key'           set progress = false
when true                           set instructions = false
when flags.search                   set searchRoster = false

# Let the server keep its own name and version on a direct route.
when route == 'direct'              set serverInfo = true
`},
	{ID: "server.session", Group: "Server", Method: "server", Side: "session",
		Summary: "What the gateway tells this server about the client when it opens a session.",
		Notes:   []string{"Nothing about the client is passed on unless a rule here says so."},
		Example: `# Present the real client, or a fixed identity.
when client.name == 'claude-code'   set clientInfo = client
when true                           set clientInfo = {'name': 'bifrost', 'version': '1'}

# Let the server ask the user questions.
when caller.role == 'admin'         set capabilities.elicitation = true

# Forward the client's _meta (only keys on the gateway's input list pass).
when true                           set meta = true
`},
	{ID: "server/discover.post", Group: "Server", Method: "server/discover", Side: "post", Event: "result",
		Summary: "How this server describes itself to the client.",
		Example: `when true                           set title = 'Team wiki'
when true                           set description = 'The team wiki, read only.'
when route == 'mux'                 set instructions = 'Search before you write. ' + upstream.instructions
`},

	{ID: "tools/list.post", Group: "Tools", Method: "tools/list", Side: "post", Event: "result",
		Summary: "Which tools are listed, and how each one looks.", Deny: "hides the tool", Notes: []string{listNote, listOrder},
		Example: `# Hide tools.
when name.startsWith('delete_')                     deny
when name == 'purge' && caller.role != 'admin'      deny "Admins only."

# Rename and redescribe. The tool is then called by its new name.
when name == 'flip'
  set name = 'toss'
  set description = 'Toss a coin.'

# Shape the parameters: a default, a ceiling, a fixed value, a hidden one.
when upstream.name == 'search'
  set params.limit.default = 20
  set params.limit.maximum = 100
  set params.space.pin = 'ENG'
  set params.debug.hide = true

when true                                           set annotations.readOnlyHint = true
`},
	{ID: "tools/call.pre", Group: "Tools", Method: "tools/call", Side: "pre",
		Summary: "The call before it is sent: whether it runs, and with what arguments.", Deny: "blocks the call",
		Notes: []string{"Runs after tools/list post, so name is the listed name and upstream.name is the server's."},
		Example: `# Block a call.
when name == 'echo'                                 deny "Not today."
when args.space == 'HR'                             deny "HR is restricted."

# Ask the user first. The call goes ahead once they confirm.
when name.startsWith('delete_')                     confirm "Run {name} on {args.id}?"

# Change the arguments.
when name == 'search' && !has(args.limit)           set args.limit = 50
when name == 'search' && has(args.limit) && args.limit > 100
  set args.limit = 100

# Give a slow tool longer.
when name == 'export'                               set timeout = duration('120s')

# Try a rule out: audit records what it would do and blocks nothing.
audit when caller.role != 'admin'                   deny "Would block non-admins."
`},
	{ID: "tools/call.post.progress", Group: "Tools", Method: "tools/call", Side: "post", Event: "progress",
		Summary: "Each progress message the server sends while the call runs.", Deny: "drops the message",
		Example: `when progress.message.contains('internal')          deny
when true                                           set message = 'Working: ' + progress.message
`},
	{ID: "tools/call.post.elicit", Group: "Tools", Method: "tools/call", Side: "post", Event: "elicit",
		Summary: "A question the server asks the user in place of a result.", Deny: "refuses the question and ends the call",
		Notes: []string{"This is the server's own question. A confirm rule in pre is the gateway's question and does not pass through here.", "Only a question (an elicitation) reaches this stage. A request for sampling or roots is refused before any rule runs."},
		Example: `when request.key == 'password'                      deny "The server asked for a password."
when upstream.name == 'create_issue'                deny "This server may not ask users questions."
when true                                           set message = 'The server asks: ' + request.message
`},
	{ID: "tools/call.post.result", Group: "Tools", Method: "tools/call", Side: "post", Event: "result",
		Summary: "The result before the client sees it.", Deny: "blocks the result",
		Notes: []string{"An error the server returns as a JSON-RPC error passes through untouched: no rule sees it."},
		Example: `# Block a result.
when has(result.structuredContent) && size(result.structuredContent) > 500
  deny "The result is too large."

# Trim fields out of the structured result.
when upstream.name == 'get_user'
  set structuredContent = drop(result.structuredContent, ['ssn', 'internal.*'])

# Keep only the structured result.
when has(result.structuredContent)                  set content = []
`},

	{ID: "prompts/list.post", Group: "Prompts", Method: "prompts/list", Side: "post", Event: "result",
		Summary: "Which prompts are listed, and how each one looks.", Deny: "hides the prompt", Notes: []string{listNote, listOrder},
		Example: `when name.startsWith('internal_')                   deny
when name == 'confluence_summary'
  set name = 'summary'
  set title = 'Wiki summary'
  set arguments.space.name = 'area'
when true                                           set description = regex.replace(description, '(?i)confluence', 'the wiki')
`},
	{ID: "prompts/get.pre", Group: "Prompts", Method: "prompts/get", Side: "pre",
		Summary: "The prompt request before it is sent.", Deny: "blocks the request",
		Example: `when args.area == 'HR'                              deny "HR prompts are restricted."
when name == 'broadcast'                            confirm "Use the prompt {name}?"
when !has(args.tone)                                set args.tone = 'neutral'
`},
	{ID: "prompts/get.post.progress", Group: "Prompts", Method: "prompts/get", Side: "post", Event: "progress",
		Summary: "Each progress message the server sends while it builds the prompt.", Deny: "drops the message",
		Example: `when true                                           set message = 'Preparing: ' + progress.message
`},
	{ID: "prompts/get.post.elicit", Group: "Prompts", Method: "prompts/get", Side: "post", Event: "elicit",
		Summary: "A question the server asks the user in place of the prompt.", Deny: "refuses the question and ends the request",
		Notes: []string{"Only a question (an elicitation) reaches this stage. A request for sampling or roots is refused before any rule runs.", "There is no result stage for prompts yet: the prompt the server returns passes through."},
		Example: `when true                                           deny "This server may not ask users questions."
`},

	{ID: "resources/list.post", Group: "Resources", Method: "resources/list", Side: "post", Event: "result",
		Summary: "Which resources are listed, and how each one looks.", Deny: "hides the resource",
		Notes: []string{listNote, "A read of a URI that is in no list runs only the deny rules here, with only uri to read.", listOrder},
		Example: `when uri.startsWith('file:///secrets/')             deny
when uri.endsWith('.log')
  set title = 'Log: ' + name
  set annotations.audience = ['assistant']
when mimeType == 'text/markdown'                    set description = 'Docs: ' + name
`},
	{ID: "resources/templates/list.post", Group: "Resources", Method: "resources/templates/list", Side: "post", Event: "result",
		Summary: "Which resource templates are listed, and how each one looks.", Deny: "hides the template", Notes: []string{listOrder},
		Example: `when uriTemplate.startsWith('file:///private/')     deny
when uriTemplate.startsWith('file:///logs/')        set description = 'Log files'
`},
	{ID: "resources/read.pre", Group: "Resources", Method: "resources/read", Side: "pre",
		Summary: "The read before it is sent.", Deny: "blocks the read",
		Notes: []string{"Runs after resources/list post."},
		Example: `when uri.contains('/payroll/')                      deny "Payroll files are restricted."
when uri.endsWith('.key')                           confirm "Read {uri}?"
`},
	{ID: "resources/read.post.progress", Group: "Resources", Method: "resources/read", Side: "post", Event: "progress",
		Summary: "Each progress message the server sends while the read runs.", Deny: "drops the message",
		Example: `when true                                           deny
`},
	{ID: "resources/read.post.elicit", Group: "Resources", Method: "resources/read", Side: "post", Event: "elicit",
		Summary: "A question the server asks the user in place of the contents.", Deny: "refuses the question and ends the read",
		Notes: []string{"Only a question (an elicitation) reaches this stage. A request for sampling or roots is refused before any rule runs."},
		Example: `when true                                           deny "This server may not ask users questions."
`},
	{ID: "resources/read.post.result", Group: "Resources", Method: "resources/read", Side: "post", Event: "result",
		Summary: "The contents before the client sees them, one text item at a time.", Deny: "blocks the contents",
		Example: `when result.text.contains('BEGIN PRIVATE KEY')      deny "The file holds a private key."
when uri.endsWith('.md')                            set text = result.text + '\n\n(reviewed)'
`},
	{ID: "skills/list.post", Group: "Resources", Method: "skills/list", Side: "post", Event: "result",
		Summary: "Which skills are listed.", Deny: "hides the skill",
		Example: `when name.startsWith('internal-')                   deny
`},
}

// notSettable are fields the engine's schema lists but a set action does
// not take: decision, reason and result are the deny and confirm actions,
// and limit is listed by the schema but refused by the compiler.
var notSettable = map[string]bool{"decision": true, "reason": true, "result": true, "limit": true}

var (
	stages []Stage
	byID   = map[string]*Stage{}
)

func init() {
	stages = make([]Stage, len(stageDefs))
	copy(stages, stageDefs)
	for i := range stages {
		s := &stages[i]
		h, ok := hooks.Hooks[s.ID]
		if !ok {
			panic("ruletext: stage " + s.ID + " is not an engine hook")
		}
		s.Property = Property(s.ID)
		if _, ok := fieldOf[s.Property]; !ok {
			panic("ruletext: Rules has no field for " + s.ID + " (" + s.Property + ")")
		}
		d := h.Schema()
		s.Item, s.Reads = d.Item, d.Variables
		for _, f := range d.Sets {
			switch {
			case f.Field == "decision":
				s.Actions = append(s.Actions, "deny")
			case f.Field == "result" && contains(d.Helpers, "elicit"):
				s.Actions = append(s.Actions, "confirm")
			}
			if !notSettable[f.Field] {
				s.Sets = append(s.Sets, f)
			}
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
		byID[s.ID] = s
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

// Stages returns every stage, in the order a request meets them.
func Stages() []Stage { return stages }

// StageOf returns the stage of a hook.
func StageOf(hook string) (Stage, bool) {
	s, ok := byID[hook]
	if !ok {
		return Stage{}, false
	}
	return *s, true
}
