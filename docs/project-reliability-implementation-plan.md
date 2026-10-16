# Project Reliability Implementation Plan

Status: Phase 0—Regression gates and Phase 1—Complete searches and honest counts
implemented and locally verified on 2026-10-16.
Phases 2–7 remain proposed. Hosted CI/oracle setup and execution remain pending.
Review date: 2026-10-16.

## Intent

Keep jira-cli as a personal Jira workbench with CLI and MCP interfaces, SQLite
caching, deterministic-first natural-language translation, optional LLM analysis,
and a dashboard that explains what happened. Prioritize trustworthy answers and
consistent behavior before expanding the feature roadmap.

This plan captures the project review of the current working tree, including
uncommitted work. Phase 0 implementation was explicitly requested on 2026-10-16.
Phase 1 was subsequently authorized. Later phases still require authorization.
Jira mutations and deployments are outside this delivery.

## Keep

- Go, the single binary, and SQLite; no separate database or vector service is
  needed for the initial improvements.
- Deterministic translation and lazy LLM initialization, so supported questions
  work without a running model.
- Central structural JQL validation, canonical quoting, and the separate Jira
  strict-parse oracle.
- Production-path translation inspection through `query --dry-run`,
  `jira_translate_query`, and the feedback trainer.
- Host-assistant recommendations based on retrieved candidates and inspected
  issue details.
- Mutation invalidation and the transition-then-get-issue regression.
- Small, independently reviewable changes rather than a wholesale rewrite.

## Observed baseline

The review ran the full race-enabled Go test suite, `go build ./...`,
`go vet ./...`, and `git diff --check`; all passed. Documentation lint failed
because `internal/nlfeedback` is absent from `AGENTS.md`. The live Jira oracle
was not run. These are review-time results, not verification of future changes.

The review-time green suite did not establish semantic correctness: some tests
expected contradictory filters, and search fixtures did not exercise token
pagination. Jira's strict parser verifies syntax and tenant vocabulary, not
agreement with the user's intent.

## Delivery order

1. **Phase 0—Regression gates:** establish CI checks and document the contracts below.
2. **Phase 1—Complete searches and honest counts**, then Phase 2 cache synchronization/isolation.
3. Add semantic regressions and typed intent/JQL construction.
4. Consolidate fallback behavior and add cached metadata resolution.
5. Improve diagnostic history, shared application services, and setup guidance.

CI and documentation corrections can proceed alongside focused correctness
work. Coordinate changes to shared files such as `tools.go`, `parser.go`, and
`cache.go`; do not assign overlapping edits concurrently.

## Phase 0: Regression gates

Primary files: `.github/workflows/build.yml`, `scripts/doc-lint.sh`, and test suites.
Implemented on 2026-10-16. Root handled scope, oracle contracts, review, and final
verification; Luna 6 implemented the bounded workflows and result runner. Existing
uncommitted work was preserved. No runtime search/count behavior changed.

- Run the hermetic Go tests on every pull request, including race detection.
- Keep build, vet, and documentation lint as required checks.
- Add a separate trusted/manual or scheduled workflow for the credential-backed
  Jira strict-parse oracle. Never expose credentials to untrusted PR code.
- Distinguish a skipped oracle from a passing oracle in reports.
- Define acceptance tests by user-visible behavior; reconsider tests that lock
  in an incorrect result rather than merely adding more passing examples.

Acceptance: a deliberately failing regression causes PR CI to fail; the ordinary
suite needs neither Jira credentials nor a live model.

### Delivered regression contracts

- `.github/workflows/build.yml` retains `Build & Vet` and `Doc Lint`, and adds
  `Race Tests` on PRs and pushes to `main`: the full uncached race suite, local
  oracle-protocol tests, and Python runner regressions. Workflow token permissions
  are read-only and jobs have time limits. The existing documentation lint script
  needed no change; the current AGENTS package inventory satisfies it.
- `.github/workflows/jql-oracle.yml` is manual only. A ref guard rejects branches
  other than the default before checkout. The oracle job checks out the dispatch
  SHA without persisted Git credentials and uses the `jira-oracle` environment.
  Credentials reach only the oracle execution step; no PR head is checked out.
- `scripts/run-jql-oracle.py` reports `PASS`, `FAIL`, or `SKIPPED`. It requires
  explicit credentials and project/issue/sprint/release fixtures, never loads
  `.env`, and fails for skipped oracle tests, zero passing oracle leaves, malformed
  or unfinished events, Go failure events, or a nonzero test exit. Missing config
  reports `SKIPPED` and exits 2. Reports count leaf cases rather than parent groups.
- Oracle positive cases require both empty `errors[]` and parsed WHERE structure.
  Project/identity/sprint/release fixtures are configured rather than tied to a
  developer's tenant. The release case now requires full validation instead of
  accepting arbitrary errors other than an unknown-field message. The local
  invalid-date check remains in the ordinary suite and cannot inflate oracle
  coverage. Protocol regressions use local HTTP fixtures for HTTP 200 validation
  errors, parsed shape/order, malformed/incomplete responses, and missing auth.
- Removed two parser success assertions that endorsed ambiguous or contradictory
  status filters. Their desired rejection behavior is recorded in explicitly
  skipped `TestParseAmbiguousStatusIntent_KnownGap` cases for Phase 3. The runtime
  semantic gap remains open and is not counted as passing coverage.
- `CONTRIBUTING.md` documents the checks, safe oracle setup, tenant vocabulary
  assumptions, skipped-result interpretation, and semantic acceptance contracts.

### Verification results (2026-10-16)

- **Passed:** `go build ./...`, `go vet ./...`, and the full
  `go test -race -count=1 ./...` suite. The two documented Phase 3 status cases
  remain explicitly skipped.
- **Passed:** `go test -race -tags=oracle -count=1 ./internal/jqloracle`
  (local protocol tests only) and 12 Python runner regression tests. The runner
  tests isolate their simulated results from real GitHub step summaries.
- **Passed:** deliberate-regression proof using a temporary Go overlay to add
  `TestPhase0GateSentinel`. The PR race-test command exited 1 for that intentional
  failure while other packages passed; the overlay left repository source
  unchanged. The ordinary suite passed after removing the overlay.
- **Passed:** workflow YAML syntax checks and execution of the actual ref-guard
  script with accepted default-branch and rejected non-default-branch inputs.
- **Passed:** `make doc-lint` and `git diff --check`.
- **Live oracle SKIPPED/unavailable:** credentials and tenant fixtures were not
  supplied. The strict runner reported `SKIPPED` and exited 2 without starting Go.
  Tagged corpus tests were compiled and their missing-credential skips inspected;
  their successful package exit is not reported as live validation.
- Some local checks initially hit sandbox restrictions on mock HTTP listeners or
  the Go cache. Approved reruns passed; the deliberate-failure proof was rerun
  with no sandbox errors so its failure was attributable to the injected test.

### Hosted activation still required

No GitHub settings were changed and no hosted run was triggered. Repository
administrators must require `Build & Vet`, `Doc Lint`, and `Race Tests` in branch
protection. Create/protect the `jira-oracle` environment with a default-branch-only
policy and store `JIRA_SERVER`, `JIRA_EMAIL`, and `JIRA_API_TOKEN` as environment
secrets, not repository secrets. Set repository variables `JIRA_PROJECT`,
`JIRA_ORACLE_ISSUE_KEY`, `JIRA_ORACLE_SPRINT_ID`, and `JIRA_ORACLE_RELEASE` to real
visible fixture data, then dispatch from the default branch. Missing fixture
variables skip execution with an explicit summary; missing secrets fail the step
with a `SKIPPED` summary. Only an explicit runner `PASS` establishes a live pass.

Phase 0's implementation and local acceptance checks are complete; hosted
activation/live validation are pending external configuration. The Phase 0 delivery
did not implement Phase 1; its subsequent implementation is recorded below.

## Phase 1: Complete searches and honest counts

Primary files: `internal/jira/client.go`, `internal/jira/client_test.go`,
`internal/mcpserver/tools.go`, `internal/mcpserver/results.go`, query templates,
and CLI query output.

- Model enhanced-search `nextPageToken` and `isLast` and follow pages as needed.
- Separate page size, requested result limit, and completeness.
- Represent returned count, whether more results exist, and an optional known
  total. Never substitute a partial page length for an exact matching total.
- For exact count requests, enumerate the complete matching issue set under an
  explicit execution policy; disclose interruptions or incomplete enumeration.
- Label any use of Jira's approximate-count endpoint as estimated.
- Expose consistent completeness information in CLI, MCP, and dashboard output.

Required regressions: multiple pages; a server page smaller than the requested
limit; omitted totals; empty results; a result limit ending mid-search; malformed
or repeated pagination tokens; failure after the first page; exact versus
estimated versus unknown counts.

Reference: [Atlassian issue-search API](https://developer.atlassian.com/cloud/jira/platform/rest/v3/api-group-issue-search/).

### Phase 1 implementation contract

Implemented on 2026-10-16. Root designed the pagination/count contract and execution
boundary, reviewed/integrated the changes, and verified the complete suite. Luna 6
implemented bounded CLI/formatting, MCP/dashboard, and search-regression work.

- `SearchJQLResult(ctx, jql, SearchOptions)` is the validated token-search boundary;
  the legacy row-only `SearchJQL` delegates to it. Page size and retained-row limit
  are independent. Default policy is 100 requested rows per page, at most 100
  pages, and an overall 60-second deadline (or earlier caller cancellation).
- `SearchInfo` records returned and enumerated rows, whether all matches are in
  the returned rows, optional knowledge of additional matches, an optional total
  and its evidence, and a stop reason. Exact counts can coexist with incomplete
  row previews. The old `SearchJQLWithTotal` integer fallback is removed.
- Token chains are followed regardless of page length. Only an explicit terminal
  `isLast` verifies enumeration; missing metadata remains unknown/incomplete.
  Tokens are opaque. Repeated, malformed, or contradictory pagination; duplicate
  or missing issue keys; and invalid/changing reported totals interrupt retrieval.
- Exact counts are selected by CLI `--count`, MCP `exact_count`, or original
  wording starting with `how many`/`count`. Other phrasings can use the explicit
  flag. Wording and executed JQL are preserved. Enumeration retains only the
  requested preview and verifies the count after reaching the terminal page.
- Unfinished counts return an error with partial rows and stop metadata. A
  reported total is labeled unverified until enumeration validates it. The model
  supports estimated labels, but this delivery never calls the approximate-count
  endpoint. Counts describe visible matches during traversal, not an atomic Jira
  snapshot; concurrent source changes can invalidate traversal.
- CLI, MCP text, and dashboard share a count/completeness formatter. Empty partial
  output never claims that there are no matches. Dashboard charts describe the
  shown rows, and historical numeric totals are treated as unknown rather than
  retroactively verified. Interrupted MCP searches keep inspectable partial output.
- This phase changes query search results. Row-only compatibility callers still
  request bounded lists and cannot expose metadata themselves. Cache membership
  reconciliation/isolation and offline completeness remain Phase 2 work. The
  separate board-scoped Agile guard and its existing retrieval path are preserved.

### Phase 1 verification results

- **Passed:** `go build ./...`, `go vet ./...`, and the integrated full
  `go test -race -count=1 -timeout=90s ./...` suite. Existing Phase 3 ambiguous
  status-intent cases remain explicitly skipped; this phase does not fix them.
- **Passed:** pagination regressions for several pages, short server pages,
  missing totals, empty terminal/unknown pages, terminal and nonterminal display
  limits, server overshoot, malformed/repeated/cyclic tokens, missing pagination,
  and stale “more” information after advancing a page.
- **Passed:** exact-count enumeration beyond the retained preview, unverified
  reported totals, estimated/unknown/exact output labels, duplicate keys and
  inconsistent totals, failure after page one, page-budget exhaustion, pre-canceled
  and in-flight contexts, and deterministic cancellation between decoded pages.
  Default-policy tests assert 100-page and 50-retained-row bounds plus HTTP
  deadlines; an earlier caller deadline is preserved.
- **Passed:** CLI and MCP automatic/explicit count selection, original wording and
  context propagation, invalid arguments, partial error previews, verified empty
  versus unknown empty output, legacy dashboard totals becoming unknown, and
  charts limited to shown rows. Only complete, error-free query row sets warm the
  issue cache; synchronization/membership reconciliation is still Phase 2.
- **Passed:** `make doc-lint`, `git diff --check`, and secret scans of the changed
  source/documentation artifacts.
- A cancellation mock initially stalled because it waited on the server request
  context without consuming the POST body. It now drains the body and bounds its
  wait; the full race suite passed after the fix. Review also corrected outdated
  count expectations without changing unrelated digest behavior.
- **Live Jira/oracle validation not run.** New behavior is tested with local
  fixtures and mock transports; no credentials were discovered, no Jira business
  operations were performed, and no JQL generator was changed. The strict-parse
  oracle remains a separate explicitly configured validation step.

Phase 1 implementation and local acceptance checks are complete. Existing local
harness artifacts were preserved. This phase has not been deployed. Phase 2 cache
synchronization/isolation and later phases remain planned.

## Phase 2: Cache synchronization and isolation

Primary files: `internal/cache/`, `internal/jira/client.go`, `cmd/summary.go`,
`cmd/weekly_status.go`, and corresponding MCP handlers.

- Separate canonical issue data from membership in a particular user/project
  result set. Include Jira site and effective identity where they affect scope.
- Detect issues leaving an open-work set after closure, reassignment, project
  movement, deletion, or loss of visibility. An open-only delta query cannot
  perform that reconciliation by itself.
- Advance synchronization checkpoints only after successful fetch and storage;
  preserve useful cached data when refresh fails.
- Filter cached summaries by the requested identity and scope.
- Include user identity in weekly-report cache keys and fingerprint report inputs
  so removals and changes in membership invalidate reports too.
- Cache each issue under its actual project, including explicit cross-project
  searches; do not stamp all query results with the default project.
- Use one definition of open work based on status category, with sufficient
  cached status metadata to apply it offline.
- Preserve mutation invalidation. Test concurrency and upstream search-index lag
  in addition to the existing sequential transition/get regression.
- Make migrations fail on unexpected errors; do not advance schema versions
  after a failed migration.

Required regressions: externally closed or reassigned issue disappears from the
next summary; two user overrides do not share membership or weekly reports; two
sites with the same issue key remain isolated; explicit other-project results
retain their project; failed refresh does not advance its checkpoint; cache
errors do not turn an already successful remote mutation into an apparent failure.

## Phase 3: Preserve intent and construct JQL structurally

Primary files: `internal/nl2jql/`, `internal/jql/`, and
`internal/llm/jqltranslate.go`.

- Preserve the distinction between the natural-language intent AST and the JQL
  AST. The first describes the requested meaning; the second makes composition,
  grouping, quoting, and terminal ordering reliable.
- Implement the deferred [JQL AST plan](FIELDCTO-2813-implementation-plan.md)
  against a freshly enumerated generator corpus.
- Introduce a typed query plan covering Boolean groups, effective ownership,
  field comparisons, date windows, sorting, and list/count/recommendation mode.
- Stop discarding meaningful `AND`/`OR` structure. Support clear combinations
  or reject ambiguity explicitly rather than compiling contradictory filters.
- Resolve `my` consistently with the documented user-override contract.
- Make supported date expressions parse into typed nodes instead of producing
  JQL-like text that necessarily falls through to the LLM.
- Specify date timezone, inclusivity, calendar versus rolling windows, and the
  distinction between currently closed, resolved during a period, and closed by
  a particular actor. Confirm history-operator boundaries with the Jira oracle.
- Replace exact-question patches with composable rules where the meaning is
  clear, retaining conservative fallback for unsupported or ambiguous requests.
- Keep local validation at every execution boundary, including the separate
  board-scoped path. Preserve existing scope behavior during migration.

Required regressions: `open or done` retains alternatives; ambiguous `open and
done` does not silently produce an impossible filter; mixed type/status Boolean
groups; user overrides; date paraphrases and boundaries; multiple sort keys;
project scope around OR; terminal ORDER BY; parse/render/parse equivalence and
fuzz cases. Compare normalized structure where formatting is immaterial.

## Phase 4: One bounded fallback and tenant metadata

Primary files: `internal/llm/`, `internal/nl2jql/fallback.go`,
`internal/config/`, `internal/jira/`, and metadata cache additions.

- Consolidate the strict and legacy JSON translation contracts into one typed
  plan output, compiled locally to JQL.
- Separate digest comment-window interpretation from search translation while
  sharing consistent temporal semantics.
- Classify unsupported intent, invalid model output, authentication errors,
  connectivity failures, timeouts, and cancellation. Use a bounded retry policy;
  do not retry unreachable providers through multiple translation prompts.
- Propagate caller context through Jira and Confluence HTTP requests.
- Define and normalize provider base URLs so documented `/v1` examples cannot
  produce `/v1/v1/chat/completions`.
- Implement the cached metadata resolver described in
  [the metadata analysis](jql-metadata-resolver-analysis.md): stable field IDs,
  types, aliases, applicability, and refresh timestamps in SQLite.
- Resolve exact IDs/names and configured aliases before scoped fuzzy matching.
  Surface ambiguity instead of silently selecting a field.
- Keep core-field dry runs functional without Jira credentials; use cached
  metadata for tenant fields and explain missing metadata.
- Evaluate embeddings only if corpus results demonstrate a need.

Required regressions: zero provider calls for supported queries; bounded calls
for unsupported queries; cancellation stops work; both base-URL forms; missing
and ambiguous custom fields; metadata isolation and stale refresh; no Jira
search from a translation-only operation.

## Phase 5: Feedback that becomes regression coverage

Primary files: `cmd/train_jql.go`, `internal/nlfeedback/`, corpus evaluation code,
and CI configuration.

- Record translation failures and skipped reviews as well as successful reviews.
- Save sanitized error classification, translation diagnostics, translator/model
  identity, and relevant non-secret configuration for reproducibility.
- Preserve the original wording and user correction. Corrections are data for
  review; collecting them must never execute JQL or change Jira.
- Add a corpus replay command and curated, sanitized fixtures usable in CI.
- Freeze clock/metadata/provider fixtures for reproducible comparisons.
- Measure semantic expectations, structural validity, scope preservation,
  completeness, deterministic coverage, acceptance rate, latency, and LLM calls.
  Acceptance statistics must expose failures rather than count only successes.

Acceptance: a previously failed or corrected example is replayable, a semantic
regression fails the suite, and private feedback/credentials stay out of Git.

## Phase 6: Diagnostic dashboard and result lifecycle

Primary files: `internal/mcpserver/store.go`, `webserver.go`, `results.go`,
handlers, and `internal/web/templates/`.

- Record failed attempts as well as successful results.
- Store original input, normalized input, exact executed JQL, separate display
  JQL, route, timestamps, duration, validation, and result completeness.
- Replace PID-based history deletion with stable instance storage and explicit
  retention controls. Decide how multiple local instances identify their stores.
- Keep liveness separate from readiness. Uptime and dashboard responsiveness
  alone do not establish MCP, cache, Jira, or provider readiness.
- Display dependency observations with checked-at timestamps. Avoid generating
  frequent paid/model or Jira traffic merely to refresh a status header.
- Label the cache statistic accurately: the current window measures ten
  issue-detail lookups, not ten natural-language search requests.

Required regressions: failed attempt appears in history; exact execution text is
retained; restart preserves history; retention respects instance ownership;
dependency failure does not report full readiness; polling does not query an LLM.

## Phase 7: Shared workflows and supported setup

- Extract shared application services incrementally for summary synchronization,
  query execution, detail retrieval, and weekly reports. CLI/MCP remain adapters.
- Keep the existing provider abstraction and SQLite persistence boundaries.
- Consolidate configuration loading and document actual precedence: process
  environment, `.env` loading without overwriting existing environment, then
  mounted secret fallback under the current implementation.
- Retain host-side Bitwarden retrieval, Keychain bootstrap, mounted Podman
  secrets, non-root containers, and loopback publication defaults.
- Treat the present shared server as a trusted single-Jira-account service.
  Distinguish multiple clients from independently authenticated Jira users.
- Align README, HOWTO, CLI help, AGENTS, and container examples on Streamable HTTP
  for the current container default; label SSE as legacy support.
- Resolve contradictory OAuth advice and test supported setup examples.
- Keep machine-specific historical notes separate from supported product setup.
- Update roadmap status and package inventories only when verified in code.

Acceptance: matching CLI/MCP operations share the same business behavior;
documented configuration examples pass contract tests; documentation lint passes.

## AGENTS.md updates applied

Applied on 2026-10-16 at the user's request. These changes describe current
implementation accurately and establish requirements for subsequent changes;
they do not implement the application phases above.

1. Global and repository delegation now assign bounded implementation that Luna 6
   can reliably handle to `gpt-6-luna`, with `fork_turns: "none"`. Tasks requiring
   deeper reasoning, careful judgment, or unresolved design stay with the root.
   Clearly specified implementation portions may be delegated after the root
   resolves those decisions. Root review/integration/verification remain required.
2. Added `internal/nlfeedback` and `train-jql`, removed the duplicate Jira package
   entry, and updated cache migrations to v7.
3. Described `searchIssuesWithTotal` and the separate board-scoped Agile guard.
4. Documented `LoadTranslation` and lazy LLM provider validation.
5. Documented Streamable HTTP at `/mcp`, retained legacy SSE guidance, and removed
   fixed tool counts and the claim of complete CLI/MCP feature parity.
6. Specified verification by change type and distinguished skipped oracle checks
   from passes. Documented that `make check` modifies files.
7. Added requirements for Boolean intent, consistent ownership/open-work semantics,
   honest counts, and original-input/exact-execution diagnostics.
8. Added requirements for cache isolation, membership reconciliation, mutation
   invalidation, and successful synchronization checkpoints.
9. Expanded credential/feedback privacy guidance, Bitwarden handling, and actual
   configuration precedence.
10. Preserved exact-wording/MCP usage requirements and distinguished explicitly
    requested development diagnostics and mock tests from business operations.
11. Linked this plan, the AST handoff, metadata proposal, and historical roadmap,
    distinguishing planned behavior from the current implementation.

## Completion and handoff

Implement one bounded phase or work package at a time. Follow the applicable
delegation policy: use focused Luna tasks for implementation it can reliably
handle; retain work requiring deeper reasoning or careful judgment with the root.
Use non-overlapping ownership, root diff review, integration, and final verification.

For each code change run the relevant behavioral tests, `go build ./...`, and
`go vet ./...`; run the full race-enabled suite for integrated milestones. Run
documentation lint and `git diff --check`. Changes to JQL construction also need
the generated-shape corpus and, when credentials are available, the strict Jira
oracle with explicit reporting of anything not run.

Do not mark a phase complete merely because the existing suite is green. Its
acceptance criteria and new regressions must pass. Do not change deferred ticket
status, commit, or deploy as a side effect of saving or reviewing this plan.
