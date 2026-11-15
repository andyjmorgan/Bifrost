# Review handover: Bifrost rules engine

You are reviewing a policy rules engine for an MCP gateway. Two agents built and attacked it before you: Claude wrote the engine, tests and docs, and Astra (Codex) reviewed it adversarially, twice. Your job is an independent review of everything both produced. Treat neither as authoritative. Find what is wrong, untested or claimed without proof.

Start in `/home/localuser/source/bifrost`, branch `rules-hooks`. Commit `ed82ea4` is pushed; the round-2 work on top of it is uncommitted in the working tree.

## Sources of truth

Read in this order:

1. The design: the "# v3: Hooks" section and the follow-up sections at the end of `/home/localuser/vaults/Datadog/AI Platform/AI Gateway/MCP Gateway - Policy Engine Intent.md`. The design is the source of truth, not the code. If the code and the design disagree, that is a finding, unless a recorded decision changed the design.
2. Recorded decisions: `docs/rules/codex-review-response.md`, `docs/rules/codex-final-comments.md`, and "Round 2" at the end of `docs/rules/v2-report.md`.
3. The protocol: MCP 2026-07-28 (stateless, `server/discover`, `input_required`) and the Skills extension. The engine uses the official Go SDK's types (`github.com/modelcontextprotocol/go-sdk` v1.8.0). Check protocol claims against the spec, not memory.

## What exists

- `pkg/hooks/`: the engine library, standalone and not wired into the gateway. Start with `hooks.go` (the hook registry), `engine.go`, `eval.go`, `tools.go`, `resources.go`, `visibility.go`, `server.go` and `cel.go`.
- `cmd/hookctl/`: a scenario runner. Build it with `go build -o bin/hookctl ./cmd/hookctl`.
- `pkg/hooks/testdata/scenarios/`: 248 JSON scenarios. `90-codex-adversarial.json` holds Astra's 70.
- `pkg/hooks/testdata/doc-checks/`: 126 scenarios, each verifying an example in the docs.
- `pkg/hooks/*_test.go`: unit, corpus, fuzz, benchmark and soak tests. `codex_adversarial_test.go` and `codex_v2_review_test.go` are Astra's.
- `docs/rules/hooks/`: 22 user-facing pages, one per hook plus an overview, in the tone of Datadog's documentation.
- `docs/rules/`: test catalogue (`test-cases.md`), Astra's reviews (`codex-review.md`, `codex-v2-review.md`, `codex-coverage-gaps.md`, `adversarial-codex.md`), and Claude's reports (`v2-report.md`, `codex-review-response.md`).
- Real fixtures, gitignored: `internal/e2e/testdata/real/`. That's 1,481 real tools (498 from Datadog's MCP server) and 936 real call examples. Tests skip when the fixtures are absent.

## Reproduce first

```sh
go build -o bin/hookctl ./cmd/hookctl
./bin/hookctl run -summary pkg/hooks/testdata/scenarios/*.json
./bin/hookctl run -summary pkg/hooks/testdata/doc-checks/*.json
go test ./pkg/hooks/ -race -count=1
```

Expected: 248, 126 and a passing race suite. If yours differ, report that first.

## What to review

Work through these, in roughly this order of value:

1. Design against code. For each hook in the design, does the engine do what the design says, including the hook order, the variables each hook can read and the fields each hook can set? Pay attention to deny-is-final, `upstream.*` staying immutable, reversible renames, mux URI prefixing and fallback, and failing closed.
2. Tests that prove less than they claim. Find assertions that pass vacuously. The runner's `notContains` on an object checks for a key, not a substring; audit every use. Look for expectations that were changed to match the implementation, not a decision. Find catalogue entries in `test-cases.md` with no real test behind them.
3. Security. Cover consent and elicitation state (signing, binding to caller, arguments and policy generation, expiry), injection through argument or upstream text, mux impersonation, resource visibility (listings, reads, directory children and fallback must agree) and secrets reaching audit records.
4. The docs. Is every claim in `docs/rules/hooks/` true of the code? Is anything important missing? Each page should read clearly to an administrator who has never seen the code.
5. Astra's findings. Did each fix address the root cause, or only the reproducing test? Spot-check V2-F01 to F05 and the original F01 to F36.
6. Lift readiness. The plan is to wire this into Bifrost, then lift it into Datadog's Go gateway. What would block that? `internal/toolsearch` is a known Bifrost dependency.

The known weak spots are listed in "Round 2" of `v2-report.md`. Confirm or refute them, but spend most of your time finding new ones.

## Rules

- Do not change engine code (`pkg/hooks/*.go` files that are not tests), existing scenarios or docs. Do not commit or push.
- Prove each finding with a failing test or scenario. Put Go tests in `pkg/hooks/fable_review_test.go` and scenarios in `pkg/hooks/testdata/scenarios/95-fable-review.json`.
- Never print or commit credentials. The real fixtures stay gitignored. Do not call external services.
- Performance work has deliberately stopped. Report a performance problem only if it affects correctness or is far outside what an MCP gateway needs.

## Report

Write `docs/rules/fable-review.md`. Use plain, front-loaded writing, with no bold for emphasis. It should have:

- a verdict in one paragraph: ready to wire into Bifrost or not, and why
- findings ranked by severity, each with the file and line, a concrete failure scenario and the test that proves it
- claims you checked and found true, kept short, so the next reader knows what was verified
- what you did not review
