# Translation feedback and replay

`train-jql` collects private review data. Every submitted query is translated
once, with the original wording preserved, and saved even when translation fails
or the review is skipped, quit, or interrupted by EOF. Neither collection nor
replay executes a Jira search or mutation. Corrected JQL is parsed locally for
review only; natural-language corrections are never sent back to a translator.

Corrected JQL is checked for local syntax and a nonempty predicate, then saved
exactly as entered. It may omit project scope because it remains review data;
the configured default project is stored separately. This does not establish
tenant validity or execution safety. Actual searches still enforce their project
scope boundary. For example, `assignee = currentUser() AND statusCategory != Done
ORDER BY updated DESC` can be recorded as a correction without adding a project
predicate or executing it. Invalid corrections show the local validation reason
and can be retried within the same attempt.

```sh
jira-cli train-jql
jira-cli train-jql report
jira-cli train-jql --feedback-file /private/path/examples.jsonl
jira-cli train-jql replay internal/nlfeedback/testdata/regressions.jsonl
jira-cli train-jql --feedback-file /private/path/examples.jsonl replay
```

The Makefile builds the current binary before running collection or reports:

```sh
make train-jql
make train-jql ARGS='-p PROJ --feedback-file /private/path/examples.jsonl'
make train-jql-report
make train-jql-replay
make train-jql-replay CORPUS=/private/path/reviewed.jsonl
```

`train-jql-replay` defaults to the checked-in sanitized regression corpus and
never calls Jira or a live model. Collection translates entered requests and may
use the configured model for unsupported wording; it gathers review data rather
than updating model weights or automatically promoting corrections.
`make test-translation` runs the focused translation/replay suites.

The default file is `~/.jira-cli/nl2jql-feedback.jsonl`. New directories use
0700 and the corpus file uses 0600, including an existing file opened for append.
Input, corrections, effective user overrides, cached tenant metadata, field
aliases, and project names can contain private information. Custom paths are
equally private. Collection refuses tracked or Git-trackable paths inside a
worktree; choose an ignored, untracked path or a location outside Git. It does
not modify Git exclusions. Only deliberately authored, sanitized fixtures belong
in Git. Never copy private feedback into the checked-in fixtures automatically.

Schema 2 records retain the original input, normalization and parse diagnostics,
generated JQL, local validation label, route, mode, outcome, sanitized failure
category, duration, provider-call count, translator version, configured
provider/model identity, default project, effective user override, and clock and
metadata snapshot. They exclude credentials, provider URLs, authenticated Jira
account/site configuration, raw upstream error messages, and provider response
bodies. Error categories are unsupported, invalid output, authentication,
connectivity, timeout, cancellation, or unknown. Provider calls count actual
completion invocations, including failed calls; initialization failures count
zero. A configured provider/model label does not mean the provider was called.

An attempt is appended before review. A completed review appends a full snapshot
with the same ID; readers use the latest snapshot in first-attempt order. Reports
and replay count each ID once. Schema 1 records remain readable, including their
older validation strings, without rewriting them. They lack reproducibility
data and require manual preparation before scored replay.

## Preparing a reviewed replay

Use the synthetic cases in
[`internal/nlfeedback/testdata/regressions.jsonl`](../internal/nlfeedback/testdata/regressions.jsonl)
as examples. After reviewing an attempt, prepare its `fixture` and add an explicit
`replay` block. Preparation is a human review step, separate from collecting a
correction. `generated_jql`, `corrected_jql`, and `correction` remain historical
data and are never automatically used as expected answers or provider responses.

```json
{
  "replay": {
    "provider": {},
    "expect": {
      "jql": "issuetype = Bug AND statusCategory != Done AND project = PROJ",
      "mode": "list",
      "route": "fast_path",
      "provider_calls": 0
    }
  }
}
```

Successful expectations require complete JQL and a mode (`list`, `count`, or
`recommendation`). Failure expectations use `error_category` instead of JQL and
mode. `provider_calls` is zero or one; `route` and `normalized_input` are optional
additional assertions. The `provider` object supplies either a `response` string
containing the versioned typed intent JSON, or an `error_category` for a simulated
failure. An absent provider response fails closed if fallback is needed. It
never initializes a live provider. Responses must be manually sanitized; the
collector does not save raw completions.

`fixture.now` is an explicit timestamp with a UTC offset. The fixture also holds
`metadata.fields`, `metadata.fetched_at`, `project_map`, and `field_aliases`.
Collection captures a clock for each attempt and passes the same snapshot into
the production translator. Replay uses the snapshot and recorded default
project/effective user, bypassing environment, `.env`, mounted secrets, cache
fetches, and provider factories. Metadata staleness is evaluated at the frozen
time. Missing fixtures or unreviewed expectations never count as a pass.

## Reading results

Reviewed ordering examples also become sanitized replay fixtures. The phrases
“ordered by last updated” and “in order of when last updated” default to
`updated DESC`; an explicit ascending direction overrides that default. The
reviewed default now applies to all updated-date aliases. Bare priority
adjectives such as “my Critical work items” retain their priority predicate.
These examples do not imply an open-status filter. For recognized core date
fields, “from oldest to newest” means `ASC`, and “from newest to oldest” means
`DESC`. The direction applies to the named field, including created, updated,
due, and resolution dates. Chronological wording on a non-date field, or wording
that omits the date field, requires clarification instead of a model guess.

Numeric and duration ordering defaults to `DESC`, including Story Points,
original work estimate (`originalEstimate`), logged work time (`timespent`), and
the amount of work remaining (`remainingEstimate`). Explicit `ASC` overrides
the default. Story Points requires applicable cached numeric field metadata;
time-tracking fields are built-in duration fields (see
[Jira's field reference](https://support.atlassian.com/jira-software-cloud/docs/jql-fields/)). All reviewed “my open”
examples use `assignee = currentUser()` (or the configured effective user) and
`statusCategory != Done`, regardless of which field orders the results. Normal
execution still requires project scope.

Keywords and ordering aliases are case-insensitive. Conservative keyword repair
accepts a unique single insertion, deletion, substitution, or adjacent letter
swap in a search command, predicate keyword, or ordering phrase. It preserves
quoted text, explicit assignee names, project identifiers, and original input;
diagnostics show the repaired normalized input separately. Repairs with
different possible meanings require clarification without a model call. This
is a bounded grammar vocabulary, not a general-purpose spelling corrector.

The reported `thare are priority` wording has a narrow relative-clause repair
after an issue noun, yielding `that are priority`; it does not rewrite project
names, assignee names, quoted values, or literal text-search terms. Clients must
still pass the original spelling, casing, and spacing verbatim.

Fallback plans must preserve recognizable leading self-ownership in every
Boolean branch and any explicit ordering that the deterministic grammar can
resolve. Missing ownership or changed ordering fails as `invalid_output` instead
of returning a broader query. These bounded guards do not verify every semantic
constraint in arbitrary unsupported language. `validation.scope=local_structure`
labels the translation tool's structural validation separately from meaning.
Translation-only successes and failures are retained as private attempt
diagnostics, with generated JQL and no executed JQL; they never search Jira.

Priority choices include Major and Minor. “Where the priority is either major
or minor” retains `priority IN (Major, Minor)` alongside the requested owner
and open-work filters. Priority filters do not add a priority sort key. “Order
by updated” sorts only by updated, while “order by priority, then updated”
preserves that sequence.
Priority and updated default to descending, with explicit ascending honored.

Natural-language list and recommendation queries without explicit sorting now
use `ORDER BY updated DESC` at the shared application translation boundary.
Explicit sort keys remain the complete requested ordering; count translations
have no implicit sort. Lower-level predicate composition and raw-JQL scope
validation do not add application ordering defaults.

Summary text and title both order by `summary ASC` unless explicitly overridden.
The reviewed “contain the words” form builds a bounded comma/OR list into one
`text ~ "first OR second OR third"` predicate. Search terms bypass keyword,
date, and project-name repair, retaining their case and spelling. Lucene
literals and the surrounding JQL string are escaped separately; malformed or
empty recognized lists require clarification without a model call.

“Child tasks of an epic” follows the user-supplied structural definition:
`parent IS NOT EMPTY AND issuetype NOT IN subtaskIssueTypes()`. This excludes
subtasks and requires a parent; it does not verify the parent's issue type.
Tenant hierarchy variations and live Jira text-search behavior remain
unverified. Translation does not fetch parent records or tenant metadata.

Replay writes a JSON report and exits nonzero unless every attempt passes its
reviewed expectations. Empty, missing, or malformed corpora also fail. CI runs
the sanitized corpus through both behavioral tests and the replay command.

- `acceptance_rate` is semantically correct successful translations divided by
  **all attempts**. Expected rejections, failed calls, unreviewed examples, and
  missing fixtures stay in that denominator.
- `expectation_pass_rate` includes correctly expected failures. A corpus can pass
  every assertion while its successful-translation acceptance is below 100%.
- `deterministic_coverage` is successful deterministic translations divided by
  all attempts, independent of whether the reviewed answer agrees.
- Per-case and aggregate metrics cover semantic constraints, Boolean grouping,
  ownership/history actors, issue dates/history bounds, explicit/default scope,
  ordering, mode, local structural validity, and constraint completeness.
  Each metric reports evaluated and passed counts; cases without semantic JQL
  expectations are not evaluated for those dimensions.
- Duration measures actual local execution time, not a frozen or simulated
  provider latency. The report includes per-case duration, aggregate duration,
  actual fixture-provider call counts, translation failures, and unscored counts.
  Collection reports mean observed latency and review acceptance separately.

The semantic gate compares complete parsed Boolean trees and ordered sort keys,
allowing commutative AND/OR term ordering. It deliberately does not infer tenant
synonyms, normalize all equivalent quoting, or prove arbitrary Boolean algebra.
Dimension-specific predicate projections help diagnose differences; complete
tree equality and mode agreement decide semantic acceptance. “Constraint
completeness” means preserving every expected translation constraint, not Jira
result completeness. No search is executed to measure result rows or counts.

These fixtures establish local regression behavior, including rejection of
valid-looking JQL that loses meaning. They do not measure a live model's quality
or prove tenant field applicability, Jira timezone semantics, or live strict
parser compatibility. Those require separately authorized evaluation/oracle work.
