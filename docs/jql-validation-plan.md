# JQL Construction and Validation Hardening

Tracking epic: [FIELDCTO-2809](https://redhat.atlassian.net/browse/FIELDCTO-2809)

## Background

On 2026-10-09, while pulling Q3 data for a quarterly review, a FIELDCTO-scoped query came back
with issues from XWING and XEINSIGHTS, and a second query reported zero results for a quarter that
actually contained 71 closed stories. Both turned out to be real defects, both are now fixed
([FIELDCTO-2807](https://redhat.atlassian.net/browse/FIELDCTO-2807),
[FIELDCTO-2808](https://redhat.atlassian.net/browse/FIELDCTO-2808)), and neither was caught by
anything other than a human noticing the output looked wrong.

The root cause in both cases was the same: JQL is assembled as text by code that cannot see the
structure it is editing. `ensureProjectScope` appended `" AND project = X"` to a string, which is
only safe if you already know the string contains no top-level `OR` and no trailing `ORDER BY` —
and a string does not tell you that. JQL has no SQL-style precedence climbing, so `a OR b` became
`a OR (b AND project = X)` and the first branch escaped the scope entirely. The same line appended
after an `ORDER BY` clause, producing JQL that Jira rejects outright with HTTP 400.

The most troubling aspect is that the risk was already understood. `QuerySystemPrompt` rule #7
(`internal/llm/query.go:66`) warns the model about this exact precedence trap in some detail.
Someone anticipated the failure and wrote it down. But a prompt instruction is a request, not an
enforcement mechanism, and in the failing query the model simply did not comply. This plan is about
replacing the request with enforcement.

## Scope

The target is all generated JQL, not only the LLM-generated kind. That distinction carries most of
the weight here, because LLM output is one of three sources and is not the riskiest one. Five
hand-built `fmt.Sprintf` sites interpolate values directly into JQL strings and carry an injection
risk the LLM path does not:

| Site | Shape |
|------|-------|
| Site | Interpolated | Caller-controlled? |
|------|--------------|--------------------|
| `cmd/find_similar.go` runFindSimilar | project | no (config) |
| `internal/mcpserver/tools.go` find-similar handler | project | no (config) |
| `internal/mcpserver/crud_tools.go` post-mutation refetch | issue key | **yes** — MCP tool argument |
| `internal/jira/client.go` GetOpenIssues | assignee, project | no |
| `internal/jira/client.go` GetOpenIssuesSince | assignee, project, time | no |
| `internal/jira/client.go` GetSprintIssues | assignee, sprint ID, project | no |
| `internal/jira/client.go` SearchByRelease | release name, project | **yes** — caller string |

Regenerate that list rather than trusting it — an earlier revision of this table listed five sites
and missed the four in `internal/jira/client.go`:

```
grep -rn 'project = %s\|key = %s\|assignee = %s\|fixVersion = %q' --include='*.go' cmd internal | grep -v _test
```

Excluding `internal/llm/query.go` (prompt text, not JQL) and `internal/jira/jql.go` (the 2816
builder, already correct).

The third source is `ensureProjectScope`, which post-processes JQL from either of the other two.
That is where both of the known defects landed, and it is the reason a guard belongs downstream of
every generator rather than inside any one of them.

An aside, recorded so nobody re-investigates it: the SQL layer was checked at the same time and
needs no work. There is no generated SQL here — all 47 SQL call sites are hand-written and
parameterized, and the two that use `fmt.Sprintf` (`internal/cache/cache.go:435` and `:720`)
interpolate nothing but placeholder lists (`?,?,?`), with values passed separately through
`args...`.

## The chokepoint

`Client.SearchJQL` (`internal/jira/client.go:205`) is a genuine single point of execution. All nine
call sites across `cmd/` and `internal/mcpserver/` funnel through it, and it is a three-line
pass-through to `searchIssues`. Every piece of JQL this tool executes passes through that one
function. Validating there covers all three sources at once, rather than patching each generator
and hoping the next one remembers.

## Approach

Three mechanisms are available, and the right answer is to combine them rather than pick one.

**A lexical guard**, local and network-free, built from the quote- and paren-aware scanning already
written for FIELDCTO-2807. Cheap and immediate, but it can only check structure, not meaning.

**Jira's own parser** at `POST /rest/api/3/jql/parse?validation=strict`. Verified during planning:
it returns HTTP 200 either way, with an `errors[]` array on invalid input and a full parsed AST on
valid input. Strict mode goes beyond syntax and rejects nonexistent fields and nonexistent values —
the two characteristic LLM hallucination modes:

```
"project = FIELDCTO AND sprintName = \"Q3\" AND storyPoints > 3"
→ Field 'sprintName' does not exist or you do not have permission to view it.
→ Field 'storyPoints' does not exist or you do not have permission to view it.

"project = FIELDCTO AND status = \"Pending Zorblax\""
→ The value 'Pending Zorblax' does not exist for the field 'status'.
```

It is authoritative — only Jira knows JQL's grammar and this tenant's vocabulary, and no local unit
test can substitute for either. But it is a network round-trip, which rules it out for the hot path
and makes it ideal for CI.

**A real JQL AST**, built with `participle`, which is already a dependency via `internal/nl2jql`.
This is the deep fix. Compose a tree and render it once, and the precedence bug is not merely
detected, it becomes unrepresentable.

**Human-visible query intent**, which is not validation at all but is the only available control for
the one failure class the other three cannot reach. See the section below.

The guiding principle is that detecting a bug is worth considerably less than making it impossible
to write. Phases 1 and 2 retire the immediate risk and are independently shippable; Phase 4 is what
actually retires the bug class.

## The one class that cannot be mechanically prevented

Everything above checks JQL against a grammar or a schema. Neither can tell you whether the query
asks the right question. From the original incident, the user asked for "my issues closed since
July 1 2026" and the translator produced:

```
project = FIELDCTO AND status = Closed AND created >= -365d
```

Under `?validation=strict` this returns zero errors and zero warnings. It is accepted as valid. And
it is wrong twice over — it filters on `created` rather than `resolutiondate`, over a rolling
365-day window rather than from a fixed date. Every phase in this plan would pass it through, and it
would return confidently incorrect data.

This is worth stating plainly because it was not a hypothetical residue: it was half of the defect
that started this work. The precedence bug and the `created >= -365d` bug were in the same string,
and only the first is mechanically preventable.

Correctness here is defined only relative to what the user meant, so no oracle can decide it. The
remaining controls are affordances — put the query in front of the user in a form where the error is
obvious. That is [FIELDCTO-2815](https://redhat.atlassian.net/browse/FIELDCTO-2815), and it should
not be described as enforcement. Overstating it would repeat the mistake of treating rule #7 as a
guarantee.

## Phases

### [FIELDCTO-2810](https://redhat.atlassian.net/browse/FIELDCTO-2810) — Lexical guard at the chokepoint (2 SP)

Extract `blankQuotedLiterals`, `topLevelKeywordIndex`, and `orderByIndex` out of
`internal/llm/jqltranslate.go`, where they are currently private and awkwardly placed, into a new
`internal/jql` package. Add `Validate()` covering balanced quotes and parentheses, `ORDER BY`
appearing once and last, no dangling operators, and the scope invariant.

That last check is the one that matters: when a default project is configured, a project predicate
must be present *and* must sit outside any top-level `OR`. Presence alone is insufficient, because
`project = X OR foo` has a project predicate and still leaks. Wire it into `SearchJQL` as a
fail-closed guard that names the offending JQL and the rule violated. It must not attempt repair —
silently rewriting a caller's query is how the original bug stayed hidden for as long as it did.

### [FIELDCTO-2811](https://redhat.atlassian.net/browse/FIELDCTO-2811) — CI oracle and golden corpus (2 SP)

Collect every JQL shape the generators can emit, plus the regression cases from 2807 and 2808, and
POST the batch to `/jql/parse?validation=strict` in CI behind a build tag so the default
`go test ./...` stays hermetic. Assert empty `errors[]` for the positive cases and populated
`errors[]` for the known-bad ones. Add hallucination negatives — a nonexistent field and a
nonexistent status value — which guard the LLM paths specifically. Where the oracle returns a parsed
structure, assert the shape as well: top-level `where` operator is `and`, and a project clause
exists at top level.

Use strict mode. The default only checks syntax, and the field- and value-existence checks are most
of the value for LLM-generated input.

Verify the test can fail. Reverting the 2807 fix should break it; a test that cannot fail is
worthless.

### [FIELDCTO-2812](https://redhat.atlassian.net/browse/FIELDCTO-2812) — Interpolation sites (3 SP)

Trace each of the five sites and determine which interpolated values are genuinely caller-supplied.
Project keys come from config and are low-risk; assignee emails, issue keys, and anything arriving
as an MCP tool argument are not. `cmd/weekly_status.go:100` already applies `fmt.Sprintf("%q", ...)`
to the email, which is the right instinct but is Go quoting rather than JQL quoting — confirm the
two agree on embedded quotes and backslashes rather than assuming they do.

Consolidate on `jql.Quote()` and `jql.QuoteIdent()`. Note that `quoteIfNeeded` in
`internal/nl2jql/parser.go` already does part of this; the goal is one implementation in the tree,
not a third.

### [FIELDCTO-2813](https://redhat.atlassian.net/browse/FIELDCTO-2813) — AST-based construction (5 SP)

Define the AST (`Clause`, `And`, `Or`, `Not`, `OrderBy`, `Query`), parse with `participle`, and
render canonically — parenthesising `Or` nodes whenever they sit under an `And`. The precedence
guarantee then lives in exactly one place instead of at every call site, and `OrderBy` as a separate
field cannot be stranded mid-expression. `ensureProjectScope` collapses to
`Query.AndClause(projectClause)`.

This is the largest item and it changes the output of every JQL path, so byte-level differences in
spacing and parenthesisation are expected. Land the Phase 2 corpus first so the migration has a
safety net that checks meaning rather than string equality — otherwise every cosmetic rendering
change looks like a regression and the real ones hide among them.

### [FIELDCTO-2816](https://redhat.atlassian.net/browse/FIELDCTO-2816) — Weekly status queried a nonexistent field (3 SP, done)

Found while tracing Phase 5 and fixed in commit `68c7a28`. The weekly-status JQL filtered on
`updatedBy`, which is not among the 786 fields `/rest/api/3/jql/autocompletedata` reports for this
instance. `/search/jql` accepts it regardless, answering HTTP 200 with an empty array, so the
feature reported "No issues found" for every window it was given — and an empty answer is
indistinguishable from a quiet week.

Replaced with a union of `status CHANGED BY` and `worklogAuthor`, which depend only on core JQL
fields. A second defect in the same line: the end date was accepted, used for the cache key and the
report title, and then omitted from the query, leaving every window open-ended.

Two things worth carrying forward. This was *hand-written* JQL, not model output — the Phase 2
oracle earns its keep against our own code, not just the LLM's. And a query returning zero rows is
not evidence of no data; this instance tolerates nonexistent fields in silence.

### [FIELDCTO-2814](https://redhat.atlassian.net/browse/FIELDCTO-2814) — Error propagation (1 SP)

During the original investigation, `jira_weekly_status` reported "No issues found" for a window that
contained 71 closed stories. "No issues found" and "the query failed" were indistinguishable from
the outside, which is most of why the root cause took as long as it did to isolate.

`searchIssues` does propagate errors correctly, so the loss is elsewhere — either in `doRequest`'s
handling of a non-2xx response or in a caller treating `(nil, err)` as an empty result. That has not
been traced yet and should not be guessed at. Jira's own message is a good diagnostic
(`"Expecting ',' but got 'AND'. (line 1, character 39)"`) and should survive to the user rather than
being flattened into a generic failure.

Despite the low points, do this early. Until it is done there is no way to tell whether the Phase 1
guard is firing at all — a rejected query that renders as "no results" looks exactly like the bug it
was added to prevent.

### [FIELDCTO-2815](https://redhat.atlassian.net/browse/FIELDCTO-2815) — Surface query intent (3 SP)

Three deterministic changes, no LLM in the verification path:

Resolve relative dates to absolute ones in the echoed JQL. This is the highest-value item and the
cheapest. `created >= -365d` is opaque; `created >= 2025-10-09` sitting next to a request that said
"since July 1 2026" is obviously wrong at a glance. The relative forms come from our own normalizer
(`internal/nl2jql/normalizer.go:178`), so they resolve without guesswork.

Echo the JQL unconditionally when the route was LLM-generated, rather than only under `--show-jql`.
The deterministic fast path is a closed grammar and can be trusted silently; the fallback tiers
cannot. The MCP server already echoes unconditionally, which is exactly how the original bug was
caught — the CLI makes it opt-in.

Surface the route label. `nl2jql.Result.Route` already carries `RouteFastPath`/`RouteFallback`/
`RouteJSON` and is currently only logged. A user told "translated by LLM fallback" has a reason to
read the JQL; a user told nothing does not.

A fourth option — rendering the JQL back to English for side-by-side comparison — is deliberately
left as "evaluate later". It is potentially the strongest signal and the only item here that would
put an LLM back into the verification path, which is a trade worth making consciously rather than by
default.

## Sequencing

Suggested order is 2810, 2811, 2814, 2815, 2812, 2813. Phase 5 (2814) is cheap and gates whether any
of the rest is observable. Phases 1 and 2 together are 4 SP and retire the immediate risk on their
own. Phase 6 (2815) is independent of the others and can land whenever. Phase 3 is worth doing
standalone rather than waiting behind Phase 4, which subsumes it but is the largest item in the set.

Total is 16 SP.

## Implementation notes

Read this before picking up any phase. It exists because an audit of these tickets found the
reasoning sound and the mechanics not — several references were stale within hours of being written.

**Line numbers here are indicative, not authoritative.** Every `file:line` in this document and in
the tickets was accurate when written, and the fixes under 2807/2808/2816 shifted the files they
point at. Locate symbols by name. Where a ticket gives a grep command, run it and work from the
result.

**Inventories here have been wrong.** FIELDCTO-2812's original list of interpolation sites missed
`internal/jira/client.go` entirely — four sites — because the search that produced it was scoped to
`cmd/` and `internal/mcpserver/`. FIELDCTO-2814 named two error paths; there are six. Treat every
list as a starting point to verify, not a specification to satisfy. "The ticket said five, I fixed
five" is the failure mode to avoid.

**Environment.** Credentials are in `~/dev/jira/.env` and are not exported by default:

```
cd ~/dev/jira && set -a && . ./.env && set +a
```

Only the Phase 2 oracle needs them. Note the LLM provider in `.env` points at
`host.containers.internal`, which resolves only inside the container network — a bare host binary
will query Jira correctly and then fail at any LLM step. That is expected, not a regression.

**Definition of done**, identical for every phase:

```
go build ./... && go vet ./... && go test ./...
```

All clean, nothing skipped. Anything touching JQL generation additionally goes through
`/rest/api/3/jql/parse?validation=strict`, asserting on `errors[]` rather than the HTTP status,
which is 200 either way. Default validation does not catch a nonexistent field — that omission is
what produced FIELDCTO-2816.

**Decisions already made.** Phase 1 threads the default project from `Client.c.project`, which is
already on the struct; do not add a parameter to `SearchJQL`. Guards fail closed and never repair,
because silently rewriting a caller's query is how the original defect stayed hidden. Same-field
multi-value renders as `IN (...)`, including for `project`.

**Where judgement is genuinely needed:** whether Go's `%q` agrees with JQL quoting on embedded
quotes and backslashes (Phase 3), and the AST node shapes (Phase 4). Write the test first and let it
answer. That is what settled every other question here.

## Out of scope

Calling `/jql/parse` at runtime on every query. It roughly doubles query latency, introduces a hard
network dependency on a path that is otherwise local-first, and buys little over the combination of
Phase 1 and Phase 4.

Making the model reliable. Rule #7 remains advisory and will stay that way. The position this plan
takes is that the model does not need to be trustworthy if its output is checked, and — where
checking is impossible, as in the intent case above — made visible instead.
