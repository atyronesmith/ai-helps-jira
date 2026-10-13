#!/usr/bin/env bash
# doc-lint — keep the docs in sync with the code.
#
# Catches the drift classes behind the 2026-10-12 doc drift:
#   A. AGENTS.md omits an internal/ package that exists on disk.
#   B. Docs reference a `make <target>` that no longer exists in the Makefile.
#   C. docs/features.md has lost its status legend / last-reviewed marker.
#
# Exits non-zero on any failure. Intended for `make doc-lint` and CI.
set -uo pipefail
cd "$(dirname "$0")/.."

fail=0
note() { printf '%s\n' "$*"; }
ok()   { printf '  ok   %s\n' "$*"; }
bad()  { printf '  FAIL %s\n' "$*"; fail=1; }

echo "== A: AGENTS.md covers every internal/ package =="
for d in internal/*/; do
  pkg="${d%/}"
  [ -n "$(ls -1 "$pkg"/*.go 2>/dev/null)" ] || continue   # skip dirs with no Go source
  name="${pkg#internal/}"
  if grep -q "internal/${name}" AGENTS.md; then
    ok "internal/${name}"
  else
    bad "internal/${name} is not mentioned in AGENTS.md"
  fi
done

echo "== B: docs reference only real make targets =="
resolved=0
for f in AGENTS.md README.md CONTRIBUTING.md; do
  [ -f "$f" ] || continue
  while IFS= read -r t; do
    [ -n "$t" ] || continue
    if grep -qE "^${t}:[[:space:]]" Makefile; then
      resolved=$((resolved + 1))
    else
      bad "$f -> 'make $t' is not a Makefile target"
    fi
  done < <(grep -ohE 'make [a-z][a-z-]+' "$f" 2>/dev/null | sed -E 's/^make //' | sort -u)
done
ok "$resolved make-target reference(s) resolve"

echo "== C: features.md keeps its status legend =="
if grep -q "Status legend" docs/features.md && grep -q "Last reviewed" docs/features.md; then
  ok "features.md has a status legend + last-reviewed date"
else
  bad "docs/features.md is missing the 'Status legend' / 'Last reviewed' marker"
fi

if [ "$fail" -eq 0 ]; then
  note "doc-lint: OK"
  exit 0
fi
note "doc-lint: FAILED"
exit 1
