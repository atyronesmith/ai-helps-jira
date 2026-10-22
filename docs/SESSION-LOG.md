# Development session log

This log contains sanitized engineering outcomes, not raw Jira responses,
provider output, runtime logs, credentials, or private feedback records.

## 2026-10-22 — Review fixes and focused commits

The user authorized committing the open changes in multiple commits to preserve
context. Root reviewed and integrated the changes, preserving the private ordering
corpus and both protected local harness files outside Git. Both protected files
still match their recorded checksums.

Review reproduced and corrected two semantic regressions. Explicit label OR
groups now preserve a trailing negation, nested group, or date predicate within
its AND branch; ungrouped mixed alternatives require clarification. Recency
idioms retain the explicitly named date field, including multiple sort terms,
and non-date chronological ordering refuses without fallback. Recognized recency
vocabulary is protected from fuzzy keyword repair. Luna added the focused tests,
which failed on the original behavior and passed after root implementation.

The implementation was committed in this dependency order:

| Commit | Result |
| --- | --- |
| `31c90fd` | Deterministic date ranges, Boolean intent, recency ordering, and sanitized replay regressions |
| `d35245a` | Bounded authored Jira activity reads, search response validation, and authenticated connection checks |
| `dfcf957` | CLI/MCP activity routing, evidence reports, retained diagnostics, and translation error guidance |
| `9516a01` | Manual dashboard connection checks, atomic instance history clearing, and browser/HTTP coverage |
| `7028c49` | Fixed Bitwarden oracle workflows, CI/local checks, and supported setup documentation |

The documentation handoff follows these commits. Final `make verify` passed,
including build, vet, the full race suite, generated-query and sanitized replay
regressions, documentation lint, both browser harnesses, 32 Python tests, and diff
checks. `go build ./...` and focused race tests for `internal/nl2jql`,
`internal/llm`, and `internal/nlfeedback` also passed. Staged Gitleaks scans found
no leaks. No live Jira/provider operation, new live oracle, deployment, restart,
or push was performed. The earlier user-reported 6/6 smoke remains historical
bounded evidence, not live verification of the review fixes.

## 2026-10-20 — Local review and translation failure follow-up

Added a confirmed **Clear log** action for saved dashboard results and attempts.
It issues one instance-scoped atomic SQL delete, preserves other owners and
future saves, and refreshes the list without resurrecting old selections. Failed
or uncertain responses retain an explicit warning; after a confirmed delete and
failed refresh, stale rows are removed without claiming that no new rows exist.
The action is manual-only and guarded by method/header/origin checks. Tests use
temporary stores; no existing user history was cleared. The dashboard now calls
the indicators **MCP tool calls** and **LLM provider** and explains unknown,
stale, and last-observed-operation semantics. Changes remain undeployed.
Final `make verify` also passed for this follow-up, including atomic-clear,
owner-isolation, cancellation, HTTP route, and dashboard interaction regressions.

Added an explicit dashboard connection-test control using the protected Jira
current-user endpoint. The bounded GET validates an authenticated account
response, returns only safe categories/timing, and updates the Jira dependency
observation. Browser requests cannot supply destinations or credentials;
redirects/retries and concurrent tests are blocked. Existing passive polling is
unchanged. Local configuration uses existing environment/mounted secrets without
requiring a project, model, Bitwarden lookup, or host Keychain. The local button
has not been deployed; its behavior is covered with mock Jira and browser tests.
Final `make verify` passed for this change, including build, vet, the complete
race suite, both dashboard JavaScript harnesses, documentation lint, 32 Python
tests, and diff checks. The HTTP integration covers both accepted and rejected
credentials through the registered dashboard route without exposing account or
credential data. No new live connection probe was run from this agent.

At this session's end, changes were uncommitted; the 2026-10-22 entry records
their later delivery. Luna reviews identified and local probes confirmed
incorrect status OR collapse, label conjunction widening, terminal-status
negation narrowing, and capitalization-dependent recency sorting. Local fixes
and semantic regressions cover those cases.

The reported activity-query failure exposed missing deterministic ISO-date
range handling, an overbroad past-tense guard, and a client bypassing MCP after
translation errors. Explicit issue-date ranges now include the entire final
day. Personal dated "worked on" requests now inspect actual authored comments,
worklogs, and status changes through a separate CLI/MCP activity path, using
inclusive calendar dates in the effective user's timezone. Project-wide visible
candidates prevent assignment, later updates, and backdated worklogs from
silently dropping evidence. Four workers, time/page/issue limits, and a shared
matching-event budget bound the scan; partial results retain evidence and
disclose unknown totals. Unsupported requests still require clarification.
MCP error responses and discovery retain original wording and instruct clients
to stop, report errors, and avoid credential discovery or shell/REST bypasses.
Client-side enforcement, deleted/restricted records, historical membership of
issues moved out of the project, and atomic snapshots remain outside this path.

Added explicit development oracle commands using the existing
Keychain/Bitwarden conventions, with hermetic credential-boundary tests and a
fixed smoke mode. Documentation now covers initialization, both service names,
and the Podman secret lifecycle. The user sees the item in iCloud and confirms
the configured lookup succeeds in their Terminal. This agent's identical lookup
still returns item-not-found outside the sandbox, indicating a context/access
difference rather than proof of a missing token. The user was given the fixed
`make test-live` command for the working Terminal. It checks date-query strict
parsing and a single visible issue's paginated activity endpoints. After fixing
the date oracle's nil-provider fixture, the user's Terminal rerun passed
**6/6 leaf oracle cases, 0 failed, 0 skipped**: one activity-read smoke, two
explicit-date query shapes, and three date-sort cases. The date harness supplies
a non-live fixture provider and asserts zero calls. No full-corpus or
full-project live success is claimed.

Final local verification passed: `make verify`, `go build ./...`, oracle-tagged
compilation, and local oracle protocol tests. Replay passes 75/75 reviewed
expectations, including 57 successful translations and 18 expected refusals,
55 deterministic successes, and six fixture-provider calls. `make verify`
includes all 32 Python launcher/runner tests. Live operations were limited to
the fixed non-mutating smoke; no Jira writes or server deployment occurred. Protected
local harness files and private corpora remain outside the changes.

## 2026-10-17 — Exit handoff

### Delivery state

HEAD at handoff preparation: `88fd70f`.

Earlier commits in this session:

| Commit | Result |
| --- | --- |
| `541e137` | Phase 6 diagnostics and durable history |
| `c068311` | Manual and optional automatic dashboard refresh |
| `45328b4` | Ownership preserved for creation-date sorting |
| `0e5a204` | Training, replay, and verification Makefile targets |
| `3c8d13a` | Updated-date ordering without model fallback |
| `88fd70f` | Review-only validation for training corrections |

The user subsequently authorized committing all session work in multiple
commits. The implementation was committed in this dependency order:

| Commit | Result |
| --- | --- |
| `00e7618` | Reviewed query ordering, search forms, typo repair, and ownership guards |
| `dd8830c` | Incomplete and abnormal provider completion rejection |
| `3585425` | Bounded, validated experimental weekly generation |
| `0199752` | Durable translation attempts, lifecycle logs, and MCP input guidance |

The documentation handoff follows those four commits. Private testcase corpora
and protected harness files remain untracked and outside the commits.
Documentation lint, local handoff links, the redacted public-file secret scan,
and diff checks passed during exit preparation. No deployment, server restart,
production Jira operation, Confluence publication, credential discovery, or
private-feedback read was performed.

### Follow-up implementation

- Collected and implemented reviewed ordering/search examples, default list
  sorting, open-work semantics, case handling, and conservative typo repair.
- Hardened weekly generation against truncation, invalid structure, repetition,
  missing/unknown groups, and ignored cancellation. Added safe classified
  failures, validated cache reads, and a new weekly cache contract version.
- Marked weekly status experimental in MCP discovery, CLI help, dashboard, and
  README documentation.
- Made successful translation-only attempts visible in durable history and
  bounded lifecycle logs, retaining original versus normalized input and no
  executed JQL. Labeled structural validation explicitly.
- Added bounded fallback ownership/order guards and preserved the reported
  typo example as a sanitized deterministic regression.

### Verification and limits

Final code verification passed: `make verify`, `go build ./...`, offline corpus
replay (**68/68**), oracle-tagged compilation, formatting, secret scan, and diff
checks. The full race suite includes the storage lifecycle, dependency failure,
passive polling, translation, and weekly generation regressions.

Earlier release-platform and container builds passed before the later fixes;
they are not verification artifacts for the final working tree. No live strict
Jira oracle or live model test was run. Phase 7 remains proposed. The targeted
semantic guards and weekly evidence instructions are not comprehensive proofs
of meaning or factual accuracy.

Intermediate failures were corrected before final verification: membership
function parsing, repair of valid leading grammar words, stale ordering/routing
fixtures, explicit-user mock arguments, and the Vertex mock JSON content type.
The [implementation plan](project-reliability-implementation-plan.md) records
the contracts, evidence, and remaining limitations in detail.

### Protected local files

These preexisting files were preserved byte-for-byte and must remain outside
Git:

| File | SHA-256 |
| --- | --- |
| `test.sh` | `f08dd99b417f0c78ba025fbf9494b384aa8522b72a645494d3956b90e6f6f918` |
| `docs/HARNESS-QWEN-REVERT.md` | `12de102ceaed10e1599188936d3f98fea0d9c270d34d9244657a35ead8e9e290` |

See [current status](STATUS.md) for the resume checklist and
[documentation index](README.md) for related references.
