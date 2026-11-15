package hooks

import (
	"fmt"
	"strings"

	"cel.dev/cel-go/cel"
	"cel.dev/cel-go/common/ast"
)

// A request reaches the gateway by one route, "direct" or "mux", and can
// carry any number of flags: independent switches the client sets on the
// request, usually as query parameters such as ?search=true. A route is a
// choice between exclusive things; a flag is on or off whatever the route.
//
// Rules read a flag as flags.<name>, for example flags.search or flags.cli.
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

// flagVars is the `flags` variable: every known flag, true or false. Names
// the engine does not know are left out, so a rule can never read them.
func flagVars(set map[string]bool) map[string]bool {
	out := make(map[string]bool, len(Flags))
	for _, name := range Flags {
		out[name] = set[name]
	}
	return out
}

// checkFlags refuses an expression that reads a flag the engine does not
// know, or that uses `flags` in any way other than flags.<name>. Without
// this, flags.serach would be an error only when a request arrived, and
// 'serach' in flags would be false for ever.
func checkFlags(a *cel.Ast) error {
	idents, selects := 0, 0
	var bad error
	ast.PostOrderVisit(a.NativeRep().Expr(), ast.NewExprVisitor(func(e ast.Expr) {
		switch e.Kind() {
		case ast.IdentKind:
			if e.AsIdent() == "flags" {
				idents++
			}
		case ast.SelectKind:
			sel := e.AsSelect()
			if op := sel.Operand(); op.Kind() == ast.IdentKind && op.AsIdent() == "flags" {
				selects++
				if bad == nil && !knownFlag(sel.FieldName()) {
					bad = fmt.Errorf("unknown flag %q; the flags are %s", sel.FieldName(), strings.Join(Flags, ", "))
				}
			}
		}
	}))
	if bad != nil {
		return bad
	}
	if idents > selects {
		return fmt.Errorf("read a flag by name, as flags.%s; the flags are %s", Flags[0], strings.Join(Flags, ", "))
	}
	return nil
}

func knownFlag(name string) bool {
	for _, f := range Flags {
		if f == name {
			return true
		}
	}
	return false
}
