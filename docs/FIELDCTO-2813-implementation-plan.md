# FIELDCTO-2813: JQL AST Implementation Plan

Status: deferred

This document is the implementation handoff for FIELDCTO-2813, the AST-based
construction phase of the [JQL validation hardening plan](jql-validation-plan.md).
The preceding lexical guard and Jira-backed oracle reduce the immediate risk,
but they do not make unsafe JQL composition impossible. This ticket supplies
that structural guarantee.

## Completion criteria

FIELDCTO-2813 is complete when the repository has a shared JQL AST, a parser
and canonical renderer, the active JQL generators compose that AST instead of
editing query strings, and the old structural scanners are no longer needed.

The core AST must represent at least:

- `Clause`
- `And`
- `Or`
- `Not`
- `OrderBy`
- `Query`

Parsing must use `github.com/alecthomas/participle/v2`, which is already a
dependency. This is separate from the AST in `internal/nl2jql`: that AST models
the supported natural-language vocabulary, while this one models JQL itself.

## Proposed design

Create the AST and parser in `internal/jql`. A `Query` owns a boolean expression
and an optional `OrderBy`; sorting must never be stored as part of a predicate
string. Provide APIs along these lines, allowing exact names to be adjusted as
the types settle:

```go
func Parse(raw string) (*Query, error)
func (q *Query) Render() string
func (q *Query) AndClause(clause Expr)
```

The parser must cover every JQL form currently emitted by the application and
the LLM fallback. At minimum, inventory and test:

- equality and comparison operators;
- `IN`, `NOT IN`, `IS`, and `IS NOT`;
- text operators such as `~` and `!~`;
- quoted strings, identifiers, numbers, dates, and relative dates;
- functions such as `currentUser()`;
- parentheses and `AND`, `OR`, and `NOT`;
- history predicates used by weekly status, including `CHANGED BY`, `AFTER`,
  and `BEFORE`;
- one terminal `ORDER BY` with one or more fields and directions.

Prefer typed boolean structure while allowing an atomic clause to retain syntax
that does not affect boolean precedence. Regenerate the inventory from current
execution and construction sites rather than relying only on this list.

The renderer must be deterministic. In particular, it must:

- use canonical whitespace and the quoting helpers in `internal/jql`;
- preserve boolean meaning;
- parenthesize an `Or` whenever it is rendered beneath an `And`;
- render `OrderBy` exactly once and after the complete where-expression;
- produce output that Jira accepts under strict validation.

## Migration sequence

1. Add parser and renderer tests before changing production call sites. Use
   the Phase 2 golden/oracle corpus as the semantic safety net; do not assert
   byte-identical output where differences are only whitespace or redundant
   parentheses.
2. Implement the AST, participle grammar, parser, and canonical renderer in
   `internal/jql`.
3. Replace `internal/llm.ensureProjectScope` string inspection and rewriting.
   Parse the query, inspect the tree for an existing project predicate, and add
   the default scope through `Query.AndClause`.
4. Migrate `internal/nl2jql` rendering so its natural-language AST constructs
   the shared JQL AST rather than concatenating predicates. Preserve its
   same-field consolidation to `IN (...)`.
5. Migrate remaining active JQL construction paths identified by a fresh
   repository-wide inventory. Keep `Client.searchIssues` as the validation
   chokepoint and preserve the separately guarded board-query path.
6. Rework `jql.Validate` to parse and inspect the AST. Preserve the project
   scope invariant and the direct `key`/`id` exemption. After parity tests pass,
   delete the superseded quote-, parenthesis-, keyword-, and `ORDER BY`
   structural scanners rather than maintaining two parsers.
7. Update the status table in `docs/jql-validation-plan.md` only after all
   tests and the strict Jira oracle pass.

If parsing a generated query fails, fail closed and return the query and parse
error. Do not silently fall back to string rewriting.

## Required regressions and properties

Tests must include these behaviors:

```text
status = Open OR assignee = currentUser()
```

plus a default project renders semantically as:

```text
(status = Open OR assignee = currentUser()) AND project = FIELDCTO
```

Likewise:

```text
status = Open ORDER BY updated DESC
```

must become semantically:

```text
status = Open AND project = FIELDCTO ORDER BY updated DESC
```

Also cover:

- existing project predicates are not duplicated;
- a project-like phrase inside a quoted literal is not treated as a predicate;
- nested `NOT`, `AND`, and `OR` retain their meaning;
- an `Or` beneath an `And` is parenthesized, while unnecessary parentheses may
  be normalized away;
- `OrderBy` cannot be placed inside or before the where-expression;
- hostile quotes and backslashes round-trip safely;
- every current generator's output parses and renders successfully;
- `Parse(Render(Parse(input)))` produces an equivalent AST.

Add a fuzz or property test for the final round-trip invariant. Equality should
compare normalized ASTs rather than original strings. Run the generated corpus
through `/rest/api/3/jql/parse?validation=strict` and assert that `errors[]` is
empty; Jira returns HTTP 200 even when this validation fails.

## Definition of done

Run all of the following successfully:

```sh
go build ./...
go vet ./...
go test ./...
```

In addition:

- the strict Jira oracle passes for every generated JQL shape;
- the FIELDCTO-2807 precedence regression is covered;
- no production path performs structural JQL surgery with concatenation;
- obsolete structural scanning code and tests are removed;
- the main validation plan records the merge commit;
- FIELDCTO-2813 receives a work comment summarizing the implementation and
  verification before it is transitioned to Closed.

FIELDCTO-2809 should remain open until this checklist is complete, unless 2813
is explicitly removed from that epic's scope and the decision is documented.
