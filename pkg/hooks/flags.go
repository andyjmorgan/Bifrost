package hooks

// A request reaches the gateway by one route, "direct" or "mux", and can
// carry any number of flags: independent switches the client sets on the
// request, usually as query parameters such as ?search=true. A route is a
// choice between exclusive things; a flag is on or off whatever the route.
//
// Rules read a flag as request.flags.<name>, for example request.flags.search.
// Every known flag is always present, false when the request did not set
// it, so a rule can read any of them without a guard. A name that is not in
// the list is refused when the rule is saved, so a typo cannot quietly be
// false.
//
// A flag is something the client says about its own request. It is a hint,
// like a client's name. A rule may use it to tailor behaviour; a rule that
// relaxes a deny because a flag is set is trusting the caller.

// Flag names.
const (
	FlagSearch = "search" // search mode: the gateway offers its search tools in place of the full listing
	FlagCLI    = "cli"    // the request comes from the command line client, through the gateway's API
)

// Flags lists every flag a rule may read. Adding a flag is adding it here:
// existing rules do not mention it, so they are unaffected.
var Flags = []string{FlagSearch, FlagCLI}

// ClientFlags are the flags a client may set on its own request, for
// example in the query string. A gateway must not take any other flag from
// the client: it sets those itself, from how the request arrived, so that a
// rule can rely on them. The cli flag is the model case: the gateway sets
// it for calls that come in through its API, and an MCP client that sends
// ?cli=true must not get it.
var ClientFlags = []string{FlagSearch}

// ClientMaySet reports whether a client is allowed to set a flag itself.
func ClientMaySet(name string) bool {
	for _, f := range ClientFlags {
		if f == name {
			return true
		}
	}
	return false
}

// flagVars is request.flags: every known flag, true or false. Names
// the engine does not know are left out, so a rule can never read them.
func flagVars(set map[string]bool) map[string]bool {
	out := make(map[string]bool, len(Flags))
	for _, name := range Flags {
		out[name] = set[name]
	}
	return out
}
