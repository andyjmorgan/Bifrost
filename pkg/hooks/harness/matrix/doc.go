// Package matrix is the end-to-end test matrix for the rules engine: every
// kind of rule and mutation, driven by every official MCP client SDK, through
// the harness gateway, to real MCP servers built on every official SDK's
// server side.
//
// The matrix is four clients by three servers:
//
//   - clients: a bare HTTP client (raw), and the Go, Python and TypeScript
//     client SDKs, through the drivers in ../clients
//   - servers: the Go, Python and TypeScript reference servers in ../servers,
//     which all follow ../servers/SPEC.md
//
// Every case runs on all twelve combinations, unless it cannot be expressed
// through a high-level SDK client (a replayed request state, a server/discover
// result, extra _meta keys); those run with the raw client only and are marked
// "not expressible" for the SDKs.
//
// Real servers record nothing, so a case asserts what the upstream received
// through the reference servers' own replies: the search tool echoes the
// arguments it received and the inspect tool reports the request as the
// server saw it. What the client received comes from the driver.
//
// A combination that fails because a client or server behaves differently is
// a finding. It is listed in the case's known differences with a one-line
// reason and skipped, unless HOOKS_KNOWN_GAPS=1. A client or server whose
// runtime is missing is skipped by name, loudly; REFSERVERS_REQUIRE (servers)
// and MATRIX_CLIENTS_REQUIRE (clients) turn those skips into failures, with
// "all" or a comma-separated list of kinds.
//
// Set up the Python and TypeScript SDKs once:
//
//	./pkg/hooks/harness/clients/setup.sh
//	./pkg/hooks/harness/servers/setup.sh
//
// Run it:
//
//	go test ./pkg/hooks/harness/matrix/ -race -count=1 -v
//
// The test ends by logging a table: cases down, the twelve combinations
// across, each cell pass, known difference, or not expressible, with counts.
package matrix
