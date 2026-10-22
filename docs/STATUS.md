# Project status and exit handoff

Updated: 2026-10-22 (America/New_York).

## Review fixes and committed follow-up — 2026-10-22

The follow-up implementation below is now committed in focused dependency order;
see the [session log](SESSION-LOG.md) for commit IDs and context. Review also
fixed two translation defects: explicit label OR groups retain negated, grouped,
and dated predicates in their original AND branches, and recency idioms honor
named date fields and multiple sort terms. Ungrouped mixed label alternatives
require clarification, and chronological wording on non-date fields refuses
without fallback. Luna supplied the additional regression tests; root review,
implementation, and final verification passed.

Final verification passed: `make verify` (build, vet, full race suite, generated
query and sanitized replay regressions, documentation lint, browser regressions,
32 Python tests, and diff checks), `go build ./...`, and focused race tests for
translation, LLM integration, and feedback replay. Staged Gitleaks scans passed.
No new live Jira oracle, provider operation, deployment, or server restart was
performed. Earlier user-reported smoke evidence below remains limited to its
six fixed cases; it is not a new verification result for these review fixes.

Only the protected `test.sh`, `docs/HARNESS-QWEN-REVERT.md`, and private
`docs/nl2jql-ordering-testcases.jsonl` remain outside these commits. The protected
harness checksums still match the recorded handoff. Rebuild/restart the server
and reconnect clients before relying on the committed behavior in a running MCP
session. Phase 7 remains proposed.

## Local follow-up — 2026-10-20

This section records the follow-up changes collected on 2026-10-20, including
work supplied by another model. They were reviewed and committed on 2026-10-22
as recorded above; the older handoff below describes the 2026-10-17 baseline.
Inspect the complete staged and unstaged diff before continuing work.

- Reviewed and corrected recency case handling, status OR preservation,
  conjunctions on multi-valued labels, and explicit terminal-status negation.
- Explicit ISO calendar ranges on issue date fields now translate
  deterministically, with an inclusive end date expressed as the exclusive
  following day. Supported timestamp wording such as "were updated between"
  no longer trips the status-history ambiguity check. Invalid ranges refuse
  before fallback; existing ordering and literal-search regressions pass.
- Personal dated "worked on" requests now route through CLI/MCP activity
  retrieval, matching account IDs against comment creation, worklog start, and
  status-history creation times. Original wording and evidence persist in
  private query history. Calendar boundaries use the effective user's profile
  timezone; inferred years and a missing-timezone UTC fallback are disclosed.
  Current assignment, status, and issue update time do not filter candidates.
  Scans cover currently visible issues in the selected current project, with
  four workers, a 120-second deadline, 1,000 candidate issues, 20 pages per event
  endpoint, and a shared 10,000 matching-event budget. Interrupted or capped
  scans retain observed evidence and disclose partial/unknown matching totals.
  Unsupported constraints require clarification. Translation-only commands
  still cannot express the report as a single issue-search JQL.
- MCP translation failures retain their cause and include stop/report and
  no-bypass guidance. Ambiguity errors additionally ask for clarification.
  These instructions cannot enforce the host client's separate shell access.
- Added explicit Bitwarden/Keychain development oracle launchers and documented
  initialization, service-name overrides, and persistent Podman secret storage.
  The user confirmed that the configured lookup works in their Terminal, while
  this agent receives item-not-found even outside the sandbox. An item is
  visible in iCloud; its location alone does not establish the cause. Live
  verification requires the working Terminal's Keychain context; no credential
  copy or rotation is required solely to fix this agent's access context.
  `make test-live` now checks strict date-query shapes plus one issue's actual
  paginated activity endpoints. The user's Terminal rerun passed **6/6 leaf
  oracle cases, 0 failed, 0 skipped**: one activity-read smoke, two explicit-date
  query shapes, and three date-sort cases. The date harness uses a non-live
  fixture provider and asserts zero provider calls. The full oracle corpus and
  a complete project activity scan remain unverified by this bounded smoke.
- Added an explicit **Test Jira connection** dashboard button backed by a
  ten-second authenticated `GET /rest/api/3/myself`. It distinguishes safe
  failure categories, reports timestamp/latency, and updates the Jira dependency
  observation. It uses server environment/mounted credentials without project,
  model, or host Keychain requirements. Redirects, retries, browser-supplied
  credentials/destinations, and concurrent probes are disallowed. Status and
  readiness polling remain passive; success does not imply project permissions
  or full server readiness. The button remains undeployed with the other local
  changes. Its final `make verify` passed, including the real HTTP route against
  mock Jira and both dashboard JavaScript harnesses; no new live probe is claimed.
- Added **Clear log** for the current instance's saved dashboard results and
  attempts, with confirmation, an atomic database clear, shared-owner visibility,
  and safe refresh/error behavior. New entries are retained after the clear;
  other instances, caches, text logs, and Jira data are unaffected. No existing
  user history was cleared during development. Dashboard labels/help now explain
  MCP tool dispatch, the configured LLM provider, and unknown/stale observations.
  The final `make verify` passed with clear-history/store/HTTP/browser coverage.

Verification passed for the local code: `make verify` (build, vet, full race
suite, documentation lint, browser regressions, 32 Python tests, and diff
checks), `go build ./...`, oracle-tagged compilation, and local oracle protocol
tests. The sanitized replay passes **75/75 reviewed expectations**, comprising
57 successful translations and 18 expected refusals; 55 successes are
deterministic and six fixture-provider calls occur. Live verification was
limited to the fixed non-mutating smoke checks run from the user's Terminal.
No Jira writes or server deployment were performed. Rebuild the server and
reconnect clients before expecting its new behavior.

## Earlier committed handoff — 2026-10-17

The session implementation is locally verified and committed in dependency
order, followed by the documentation handoff. See the commit list in the
[session log](SESSION-LOG.md). The changes remain undeployed. Private testcase
corpora and the protected harness files remain untracked and outside the
commits. The running MCP server has not been restarted by this session.
Phase 7 remains proposed and requires separate authorization.

## Current work

- Phase 6 durable history, retention, dependency observations, and dashboard
  refresh controls were committed earlier. See the
  [implementation plan](project-reliability-implementation-plan.md).
- The committed translation changes cover reviewed date/numeric/text ordering,
  default `updated DESC`, consistent open-work semantics, conservative typo
  repair, priority alternatives, literal text searches, and epic-related forms.
- `jira_translate_query` preserves exact original wording. Successful and failed
  calls retain private diagnostic attempts without executing Jira searches.
  The reported relative-priority typo now takes the deterministic path and
  preserves ownership and descending priority order. Bounded fallback guards
  reject lost leading ownership and changed recognizable ordering.
- Weekly status is **experimental**. Generation honors cancellation and a
  90-second deadline, rejects incomplete/repetitive/invalid output before saving
  or publishing, and omits raw model output from weekly errors and logs.
  Generated narratives still require review against source evidence.

## Latest verification

All of the following passed for the final code changes:

- `make verify`: native build, vet, full race suite, documentation lint,
  dashboard browser regressions, and diff checks.
- `go build ./...` and CLI offline replay of the sanitized corpus: **68/68**
  reviewed expectations, including 52 successful translations and 16 expected
  failures; 50 deterministic successes and six fixture-provider calls.
- Compilation of all oracle-tagged packages, formatting checks, a redacted
  changed-file secret scan, and protected-file checksum comparisons.

Earlier in the session, all four release-platform builds and a container image
build passed. Those builds preceded the later weekly-generation and translation
visibility fixes; rebuild those artifacts before using the later changes.

No live Jira/provider/Confluence operation or live strict Jira oracle was run.
Hosted CI, end-to-end Qwen behavior, and deployed-server behavior remain
unverified. Local mocks and structural validation do not prove arbitrary
natural-language semantic correctness or narrative accuracy.

## Resume checklist

1. Inspect `git status`, `git diff`, and `git diff --cached` before editing.
   Preserve all existing work and read [AGENTS.md](../AGENTS.md).
2. Review the [session log](SESSION-LOG.md) and detailed
   [implementation plan](project-reliability-implementation-plan.md), especially
   the ordering, weekly-generation, and translation-visibility follow-ups.
3. Keep credentials, private feedback/testcase corpora, runtime logs, databases,
   and local MCP configuration outside Git. Do not discover credentials to
   enable an oracle run.
4. Preserve the preexisting `test.sh` and `docs/HARNESS-QWEN-REVERT.md` unchanged
   and outside Git. Both checksums are recorded in the session
   log. Leave unrelated ignored local files alone.
5. Review the ordered commits before any separately authorized deployment.
   A server rebuild/restart and client reconnection are needed to use the latest
   MCP behavior; none is authorized by this handoff alone.

The [documentation index](README.md) links the maintained project references.
