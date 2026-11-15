// Package harness is an end-to-end test rig for the rules engine in
// pkg/hooks. It runs real MCP clients over real HTTP on localhost, through a
// thin test gateway built on the engine, to scriptable mock upstream MCP
// servers.
//
// It is test support, not a gateway. Nothing here is hardened or meant for
// production traffic.
//
// The parts:
//
//   - Upstream (upstream.go) is a mock MCP server on raw net/http. Every
//     tool, resource and prompt has a scripted Reply, so a server can
//     misbehave: stream or not, send progress, logs and unknown
//     notifications, stall, ask for input, return a task handle, return
//     errors, bad statuses and malformed bodies, or end a stream without an
//     answer. It speaks 2026-07-28 or an older version, and records every
//     request it receives.
//   - Gateway (gateway.go) is the front door: 2026-07-28 Streamable HTTP in,
//     the engine in the middle, HTTP out to the upstreams. It serves the 1:1
//     route at /direct/<slug>, the mux at /mux and search mode at /search.
//     Every accepted request is answered with an SSE stream.
//   - adapter.go holds every call into the engine. The reply path hands the
//     upstream's events to the engine's stream entry points (ToolReplyStream,
//     PromptReplyStream, ReadReplyStream) and relays what they yield.
//   - Connect (client.go) returns a session of the official Go SDK client.
//     Raw and RawStream are a bare HTTP client for what the SDK hides:
//     status codes, headers, SSE frames, their order and keep-alives.
//
// Tests assert in three places: what the client received, what the engine
// recorded (Gateway.Records: hits, issues and outcomes), and what the
// upstream actually received (Upstream.Requests).
//
// The mock servers, the gateway and the runner are Go. The cases are not:
// they are JSON, and the Go here only runs them. Run everything with:
//
//	go test ./pkg/hooks/harness/... -race -count=1
//
// # The golden set
//
// Every case is JSON under pkg/hooks/testdata; its README.md is the format.
// TestGolden (golden_test.go) is the one runner that takes the set over a
// wire: every case, once per client harness in Clients, against the mock
// upstream or each reference server, as the case says. The engine harness
// (hookctl) reads the same files with no wire.
//
//   - Clients (driver.go) is the client registry: the raw HTTP client and the
//     Go SDK, plus the Python and TypeScript SDK drivers of the clients
//     package, which drivers_test.go adapts and registers. A client whose
//     runtime is missing is reported by name.
//   - The runner decides nothing about what to skip. A case or a step runs
//     on every harness unless it carries an `exclude` entry for that harness,
//     with a reason, or the case is an outlier (`only`) for another harness.
//     One that cannot run and has no entry fails.
//   - A step excluded for a client harness is made by the raw client in its
//     place, so the steps after it still have what they need.
//
// The summary is logged at the end of the test; run with -v to read it.
// HOOKS_RUN_EXCLUDED=1 runs excluded cells and steps and fails on any that
// pass. HARNESS_SUGGEST_EXCLUDES=1 prints what each missing exclusion would
// have to say. HARNESS_DEBUG=1 logs what each client returned.
//
// The Go tests in machinery_test.go are not cases; each says why.
package harness
