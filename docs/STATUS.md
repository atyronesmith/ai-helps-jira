# Project status and exit handoff

Updated: 2026-10-17 (America/New_York).

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
