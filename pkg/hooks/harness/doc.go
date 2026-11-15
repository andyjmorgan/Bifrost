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
// Everything here is Go: the mock servers, the gateway, the clients and the
// tests. Run it with:
//
//	go test ./pkg/hooks/harness/ -race -count=1
//
// Tests that assert behaviour the engine does not have yet are skipped
// unless HOOKS_KNOWN_GAPS=1 is set, the same convention as pkg/hooks. They
// assert the decided behaviour and fail until the engine catches up.
//
// # Scenario parity
//
// TestScenarioParity (parity_test.go) replays every scenario in
// pkg/hooks/testdata/scenarios and every doc check in
// pkg/hooks/testdata/doc-checks end to end, once per client in Clients.
// The mock upstreams serve the scenario's own catalogs and answer with its
// own upstream replies; the scenario's expectations are checked by the
// scenario runner's own checker. A new scenario runs here with no new code.
//
//   - Clients (driver.go) is the client registry: the raw HTTP client and the
//     Go SDK, plus the Python and TypeScript SDK drivers of the clients
//     package, which drivers_test.go adapts and registers. A client whose
//     runtime is missing is reported by name as "CLIENT NOT RUN".
//   - An SDK makes a confirmation's rounds itself, so consecutive steps that
//     are exactly what an SDK would send next are folded into one call with
//     scripted answers. A step an SDK cannot express is made by the raw
//     client and counted, with the reason, in the summary.
//   - engineOnly and knownDifferences (parity_lists_test.go) are the two
//     explicit lists: scenarios with no end-to-end form, and scenarios whose
//     end-to-end result differs from the engine-level result for a client.
//     The test fails if a scenario cannot run and is not listed, or is
//     listed and no longer needs to be.
//
// The summary is logged at the end of the test; run with -v to read it.
// Maintenance aids: HARNESS_PRINT_ENGINE_ONLY=1 prints the entries
// engineOnly should hold, HARNESS_PRINT_DIFFERENCES=1 (with
// HOOKS_KNOWN_GAPS=1) prints every scenario that differs with its messages,
// and HARNESS_DEBUG=1 logs what each client returned.
package harness
