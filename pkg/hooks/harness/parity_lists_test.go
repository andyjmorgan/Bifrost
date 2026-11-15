package harness

// engineOnly is every scenario that has no end-to-end run, with the reason.
// The key is "<directory>/<file>: <scenario name>". The parity test fails
// if a scenario cannot run end to end and is not listed here, or is listed
// here and can.
var engineOnly = map[string]string{
	"doc-checks/resources.json: resources/result E4 skill files are rewritten at list time with digests recomputed":                                  "deferred extension: skills",
	"doc-checks/resources.json: resources/result nuance: a rule that reads the caller is not applied to skill files":                                 "deferred extension: skills",
	"doc-checks/resources.json: skills/filter E1 allow list by name":                                                                                 "deferred extension: skills",
	"doc-checks/resources.json: skills/filter E2 deny one of two skills with the same name":                                                          "deferred extension: skills",
	"doc-checks/resources.json: skills/filter E3 deny by frontmatter":                                                                                "deferred extension: skills",
	"doc-checks/resources.json: skills/filter nuance: nested skills go with the parent; directory reads drop denied folders":                         "deferred extension: skills",
	"scenarios/06-resources-skills-mux.json: ADV-47 a rewrite rule that depends on the request is not applied to skills (digests would never match)": "deferred extension: skills",
	"scenarios/06-resources-skills-mux.json: RH-MUX-01/02/05/11 mux: prefixed listings and skills, digests unchanged":                                "deferred extension: skills",
	"scenarios/06-resources-skills-mux.json: RH-MUX-09/10/11/13/14 fallback resolution: unique, ambiguous, unknown, filtered":                        "deferred extension: skills",
	"scenarios/06-resources-skills-mux.json: RH-SKL-01/02/06/12 deny a skill by name: folder, nested skill and listings":                             "deferred extension: skills",
	"scenarios/06-resources-skills-mux.json: RH-SKL-03/04 name and description shortcuts; a uri rule denies one of two same-named skills":            "deferred extension: skills",
	"scenarios/06-resources-skills-mux.json: RH-SKL-07/08 a rewrite recomputes digest and size and copies frontmatter into the entry":                "deferred extension: skills",
	"scenarios/06-resources-skills-mux.json: RH-SKL-09 / ADV-31 a rewrite that changes the name is rejected and nothing is partly applied":           "deferred extension: skills",
	"scenarios/06-resources-skills-mux.json: RH-SKL-10 denying one file inside a skill is allowed and leaves the manifest alone":                     "deferred extension: skills",
	"scenarios/07-search-fresh-limits.json: ADV-29 a manifest entry outside the skill folder is dropped":                                             "deferred extension: skills",
	"scenarios/07-search-fresh-limits.json: RH-FRS-01 list results carry ttlMs and cacheScope private":                                               "deferred extension: skills",
	"scenarios/08-gaps.json: ADV-30 the rewrite pipeline never touches a file the caller cannot read":                                                "deferred extension: skills",
	"scenarios/09-v2.json: server/connect is carried in every forward, with the _meta policy applied":                                                "the scenario's client and the client declared in the request's _meta differ; on the wire _meta is the only declaration",
	"scenarios/90-codex-adversarial.json: CX-24 clock dependent skill rewrite is skipped during reads as well as listings":                           "deferred extension: skills",
	"scenarios/90-codex-adversarial.json: CX-25 caller dependent skill rewrite is skipped during reads as well as listings":                          "deferred extension: skills",
	"scenarios/90-codex-adversarial.json: CX-26 rejected skill name rewrite also preserves SKILL.md read bytes":                                      "deferred extension: skills",
	"scenarios/90-codex-adversarial.json: CX-27 skill transaction rollback covers earlier supporting file reads":                                     "deferred extension: skills",
	"scenarios/90-codex-adversarial.json: CX-28 empty text file rewrite updates its advertised digest and size":                                      "deferred extension: skills",
	"scenarios/90-codex-adversarial.json: CX-29 nested SKILL.md rewrite does not replace enclosing skill frontmatter":                                "deferred extension: skills",
	"scenarios/90-codex-adversarial.json: CX-30 dynamic skills are never rewritten (decision F21): entry and file both stay unchanged":               "deferred extension: skills",
	"scenarios/90-codex-adversarial.json: CX-31 rewritten SKILL.md must retain required description frontmatter":                                     "deferred extension: skills",
	"scenarios/90-codex-adversarial.json: CX-37 upstream skill list size cap":                                                                        "deferred extension: skills",
	"scenarios/90-codex-adversarial.json: CX-40 direct search results use names executable on that direct route":                                     "a search-mode tool on a route that is not search mode, which no client can reach",
	"scenarios/90-codex-adversarial.json: CX-43 listing order is URI within each slug (decision F35, byte-consistent caching)":                       "deferred extension: skills",
	"scenarios/90-codex-adversarial.json: CX-67 discover advertises Skills extension before exposing skills methods":                                 "deferred extension: skills",
}

// knownDifferences is every scenario whose end-to-end result differs from
// its engine-level result for one client. The key is
// "<client>|<directory>/<file>: <scenario name>". Each is a finding: the
// subtest runs, and fails, only with HOOKS_KNOWN_GAPS=1.
const (
	// The Go SDK's typed tool result cannot carry structuredContent: null.
	goSDKDropsNull = "the Go SDK's typed result drops a structuredContent that is null"
	// The Python and TypeScript SDKs validate a tool result against the
	// schema. The engine passes a nonconforming upstream result through
	// unchanged (no runtime validation), and these SDKs then reject it, or
	// return it without the part that did not fit.
	strictToolResult = "the SDK validates tool results strictly: a result with no content array, or with structuredContent that is not an object, is rejected or loses that part"
	// The same strictness on the questions an upstream asks: an elicitation
	// request the SDK's schema does not accept makes the whole
	// input_required result invalid, so the client is never asked.
	strictInputRequest = "the SDK does not accept this upstream input request as valid, so the call fails at the client instead of asking the question"
	// The Python SDK's typed result always has isError, true or false.
	pythonAlwaysIsError = "the Python SDK's typed result always carries isError, so 'isError is absent' cannot be seen"
	// The TypeScript SDK returns a result without the resultType field.
	typescriptNoResultType = "the TypeScript SDK returns the result without its resultType field"
	// The Python SDK refuses a server-sent event over 1 MiB. The real
	// Datadog listing is about 1.4 MB, sent as one event.
	pythonEventLimit = "the Python SDK refuses a server-sent event over 1 MiB; the real Datadog tools/list result is about 1.4 MB in one event"
	// The engine passes a content block of a type it does not know on
	// unchanged. Every SDK then rejects the whole result: the Go SDK with
	// "unrecognized content type", the other two in validation.
	unknownContentBlock = "every SDK rejects a tool result that holds a content block of an unknown type, so the whole call fails at the client"
)

var knownDifferences = map[string]string{
	"go-sdk|scenarios/90-codex-adversarial.json: CX-69 valid scalar and null structuredContent preserve their JSON types":                                                    goSDKDropsNull,
	"go-sdk|scenarios/95-fable-review.json: FR-U a content block of an unknown type is passed on unchanged and recorded by shape, never by value":                            unknownContentBlock,
	"python-sdk|doc-checks/overview.json: overview: cap a list with a ternary":                                                                                               strictToolResult,
	"python-sdk|doc-checks/prompts.json: prompts v2: prompts/input sees upstream names and patches; eval errors are generic":                                                 strictInputRequest,
	"python-sdk|doc-checks/tools-input.json: tools/input E3 refuse URL-mode requests":                                                                                        strictInputRequest,
	"python-sdk|doc-checks/tools-input.json: tools/input nuance: request.key and args under upstream names":                                                                  strictInputRequest,
	"python-sdk|doc-checks/tools-input.json: tools/input v2: args are the forwarded arguments; state-only replies are wrapped; answers forwarded without upstream state":     strictInputRequest,
	"python-sdk|doc-checks/tools-result.json: tools/result E1 drop fields, the text copy, and the matching schema":                                                           pythonAlwaysIsError,
	"python-sdk|doc-checks/tools-result.json: tools/result E2 keep chosen fields and cap the list":                                                                           strictToolResult,
	"python-sdk|doc-checks/tools-result.json: tools/result v2: args are the forwarded arguments; result.meta; structuredContent keeps its type":                              strictToolResult,
	"python-sdk|scenarios/04-elicit-input-results.json: RH-RES-06/07 no runtime schema validation; a nonconforming upstream passes through":                                  strictToolResult,
	"python-sdk|scenarios/09-v2.json: prompts/input and resources/input reword or deny upstream prompts":                                                                     strictInputRequest,
	"python-sdk|scenarios/12-result-drop.json: DROP-09 YAML text content: no rule touches it, nothing throws, it passes through byte-identical":                              pythonAlwaysIsError,
	"python-sdk|scenarios/13-datadog-telemetry.json: DD-04 real Datadog catalog: telemetry is gone from every listed tool and every call carries the pin":                    pythonEventLimit,
	"python-sdk|scenarios/90-codex-adversarial.json: CX-65 tool result meta variable exposes upstream _meta under documented name":                                           pythonAlwaysIsError,
	"python-sdk|scenarios/90-codex-adversarial.json: CX-69 valid scalar and null structuredContent preserve their JSON types":                                                strictToolResult,
	"python-sdk|scenarios/95-fable-review.json: FR-01 control: a decline of the upstream server's own prompt is passed through to the server":                                strictInputRequest,
	"python-sdk|scenarios/95-fable-review.json: FR-U a content block of an unknown type is passed on unchanged and recorded by shape, never by value":                        unknownContentBlock,
	"typescript-sdk|doc-checks/claims.json: claims: unknown arguments pass through; non-elicitation input method; result default deny; _meta kept":                           typescriptNoResultType,
	"typescript-sdk|doc-checks/overview.json: overview: cap a list with a ternary":                                                                                           strictToolResult,
	"typescript-sdk|doc-checks/prompts.json: prompts v2: prompts/input sees upstream names and patches; eval errors are generic":                                             strictInputRequest,
	"typescript-sdk|doc-checks/tools-call.json: tools/call E1 deny an argument value with a reason; effective arguments include defaults":                                    typescriptNoResultType,
	"typescript-sdk|doc-checks/tools-call.json: tools/call E4 set a hidden parameter to the caller":                                                                          typescriptNoResultType,
	"typescript-sdk|doc-checks/tools-input.json: tools/input E2 refuse upstream prompts for API keys":                                                                        typescriptNoResultType,
	"typescript-sdk|doc-checks/tools-input.json: tools/input E3 refuse URL-mode requests":                                                                                    strictInputRequest,
	"typescript-sdk|doc-checks/tools-input.json: tools/input nuance: request.key and args under upstream names":                                                              strictInputRequest,
	"typescript-sdk|doc-checks/tools-input.json: tools/input v2: args are the forwarded arguments; state-only replies are wrapped; answers forwarded without upstream state": strictInputRequest,
	"typescript-sdk|doc-checks/tools-result.json: tools/result E1 drop fields, the text copy, and the matching schema":                                                       typescriptNoResultType,
	"typescript-sdk|doc-checks/tools-result.json: tools/result E2 keep chosen fields and cap the list":                                                                       strictToolResult,
	"typescript-sdk|doc-checks/tools-result.json: tools/result E3 replace upstream error details":                                                                            typescriptNoResultType,
	"typescript-sdk|doc-checks/tools-result.json: tools/result E4 deny a result by its text":                                                                                 typescriptNoResultType,
	"typescript-sdk|doc-checks/tools-result.json: tools/result v2: args are the forwarded arguments; result.meta; structuredContent keeps its type":                          strictToolResult,
	"typescript-sdk|scenarios/04-elicit-input-results.json: RH-RES-06/07 no runtime schema validation; a nonconforming upstream passes through":                              strictToolResult,
	"typescript-sdk|scenarios/09-v2.json: prompts/input and resources/input reword or deny upstream prompts":                                                                 strictInputRequest,
	"typescript-sdk|scenarios/95-fable-review.json: FR-01 control: a decline of the upstream server's own prompt is passed through to the server":                            strictInputRequest,
	"typescript-sdk|scenarios/95-fable-review.json: FR-U a content block of an unknown type is passed on unchanged and recorded by shape, never by value":                    unknownContentBlock,
}
