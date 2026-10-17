# JQL Metadata Resolver: Article Analysis and Recommendation

## Implementation status (2026-10-16)

Phase 4 now implements the bounded version of this recommendation: a single typed
model contract, local AST compilation, scoped SQLite catalogs, explicit
`metadata refresh`, exact IDs/configured aliases/names, and conservative fuzzy
resolution with ambiguity errors. Optional verified project/work-type refresh
uses `/projects/fields`; site-only catalogs retain unknown applicability. The
latest refresh replaces the catalog, including removals. Translation-only paths
read snapshots without Jira requests. Stale snapshots stay usable offline with
freshness diagnostics. No embedding index, background refresh, or automatic
strict parse call was added. See [the reliability plan](project-reliability-implementation-plan.md#phase-4-one-bounded-fallback-and-tenant-metadata)
for delivered contracts, verification, and remaining limitations. The analysis
below records the original proposal and its rationale.

## Conclusion

The metadata-grounding idea in Debi Mahapatra's article, [Building an AI-Powered JQL Generator Using AWS Bedrock, Claude and Vector Search](https://medium.com/@mahapatradebi17/building-an-ai-powered-jql-generator-using-aws-bedrock-claude-and-vector-search-a818dbf28d0c), is worth adopting in a narrower form. The article correctly identifies tenant-specific Jira fields and business vocabulary as a major source of natural-language-to-JQL errors. Its proposed implementation should not replace this repository's deterministic AST, validation, project-scope enforcement, or feedback harness.

The article's worked example demonstrates the risk of using vector retrieval as the primary semantic layer. Its retrieval maps terms such as `gladiator` to Reporter, `greater` to Rank, and `closed` to an unrelated status example. The final generated JQL also changes the Boolean meaning of the request: a closed bug can match merely because it has more than 30 story points. There is no confidence threshold, ambiguity handling, structural compilation, strict Jira validation, or evaluation corpus.

The recommended architecture is:

```text
Natural language
  -> deterministic normalizer and typed AST
  -> tenant metadata resolver for unknown fields and business terms
  -> constrained LLM fallback only when needed
  -> typed intermediate representation
  -> deterministic JQL renderer
  -> local structural and scope validation
  -> Jira strict parse validation when online
  -> Jira execution
```

Vector search should be optional and limited to resolving tenant vocabulary. Exact aliases, field IDs, lexical search, and fuzzy matching should run first. Operators, Boolean grouping, dates, current-user ownership, project scoping, and `ORDER BY` placement should remain deterministic.

## Jira field-discovery APIs

Jira Cloud can provide the field catalog directly. The important distinction is between fields visible across the Jira site and fields applicable to a particular project and work type.

### Site-visible field catalog

- `GET /rest/api/3/field` returns system and custom fields visible to the calling user.
- `GET /rest/api/3/field/search` is the paginated alternative. It returns metadata such as field ID, name, description, schema, searcher, contexts count, projects count, and usage information. It can filter by name/description, field type, IDs, and project IDs.
- These results are permission-filtered. They are not an authoritative statement that every returned custom field is valid for every project or work type.

Reference: [Jira Cloud issue-field APIs](https://developer.atlassian.com/cloud/jira/platform/rest/v3/api-group-issue-fields/).

### Project and work-type applicability

- `GET /rest/api/3/projects/fields` returns fields available for requested project IDs and work-type IDs. Atlassian currently marks this endpoint experimental.
- Custom-field context APIs expose the projects and work types associated with a custom field. Some context operations require elevated Jira permissions and therefore should not be assumed available to an ordinary API-token user.
- `GET /rest/api/3/field/search` with project filtering can be used as a less precise fallback when the project/work-type endpoint is unavailable.

Reference: [Jira Cloud custom-field contexts](https://developer.atlassian.com/cloud/jira/platform/rest/v3/api-group-issue-custom-field-contexts/).

### Strict JQL validation

`POST /rest/api/3/jql/parse?validation=strict` parses and validates generated JQL in the current user's context. It returns errors and a parsed structural representation. This should remain the online oracle after local, network-free validation.

Reference: [Jira Cloud JQL parse API](https://developer.atlassian.com/cloud/jira/platform/rest/v3/api-group-jql/).

## Recommended implementation for this repository

1. Add Jira client methods for the paginated field catalog and, where available, project/work-type field applicability.
2. Persist normalized metadata in SQLite: field ID, stable ID, display name, description, schema type, searcher, aliases, project/work-type scope, and refresh timestamp.
3. Refresh metadata explicitly, periodically, or when Jira rejects a supposedly valid field. Do not fetch it for every translation.
4. Let tokenless `query --dry-run` use cached metadata. If no cache exists, deterministic core-field queries should still work; tenant-field queries should identify the missing metadata rather than invent a field.
5. Resolve a field in this order:
   - canonical system field or custom-field ID;
   - exact configured alias;
   - exact Jira display name;
   - lexical/fuzzy match constrained to the active project and work type;
   - optional embedding search over complete noun phrases;
   - explicit ambiguity result when candidates are too close.
6. Have the LLM return a typed filter representation containing resolved field IDs, operators, values, and Boolean groups. Never ask it to be the final authority on quoting, grouping, scope, or ordering.
7. Compile that representation to JQL locally, enforce the configured project scope, and run the existing structural validator.
8. When Jira credentials and network access are available, send the result to the strict Jira parse endpoint before execution or as part of oracle/integration testing.
9. Feed translation attempts and user corrections into the existing feedback corpus. Measure parse validity, field-resolution accuracy, Boolean equivalence, scope preservation, Jira validation success, acceptance rate, latency, and LLM-call count.

## Storage recommendation

PostgreSQL and pgvector are unnecessary for the initial implementation. A Jira tenant normally has a metadata collection small enough for SQLite tables, FTS5, exact aliases, and the repository's existing fuzzy matching. An embedding column or separate local vector index can be added later if the feedback corpus shows that lexical resolution is insufficient.

The metadata resolver should be an enrichment layer between deterministic parsing and fallback generation, not a replacement for the deterministic-first design.
