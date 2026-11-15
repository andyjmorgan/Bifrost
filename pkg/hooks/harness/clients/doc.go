// Package clients runs the official MCP client SDKs against the harness
// gateway, so every rule and every change can be checked with the clients
// people actually use, not only with a hand-written HTTP client.
//
// Three SDKs sit behind one Driver interface:
//
//   - Go: github.com/modelcontextprotocol/go-sdk, in this process.
//   - Python: the `mcp` package, driven as a subprocess (python/driver.py).
//   - TypeScript: @modelcontextprotocol/client, driven as a subprocess
//     (typescript/driver.mjs). The earlier 1.x package,
//     @modelcontextprotocol/sdk, has a driver too (--sdk=v1); it predates
//     2026-07-28 and is kept so that failure stays on record.
//
// The subprocess drivers speak a line protocol: one JSON request per line on
// stdin, one JSON reply per line on stdout. See the header of either driver
// for the shapes. The Go tests are the only code; the Python and JavaScript
// files are thin adapters around each SDK and contain no assertions.
//
// Install the Python and TypeScript SDKs once, at the versions pinned here:
//
//	./pkg/hooks/harness/clients/setup.sh
//
// The drivers are what the golden runner in the harness package uses to take
// every JSON case through each SDK. The tests in this package are the client
// outliers: the few checks that need code to drive one client in its own
// way (see adherence_test.go). Run them with:
//
//	go test ./pkg/hooks/harness/clients/ -count=1 -v
//
// A client whose runtime or SDK is missing is skipped loudly, by name, and
// never silently. The outliers log a table of every check against every
// client: conforms, differs (with what the client did) or not supported.
package clients
