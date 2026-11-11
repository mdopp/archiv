#!/usr/bin/env sh
# One cap on the production lines (laib#1170), as laib's scripts/loc.sh has
# one per program: the cap and the folders it counts are the one line of
# scripts/loc.txt, the reason beside it. Production is a .go file that is
# not a _test.go and a .py file that is not a test_*.py or *_test.py,
# outside testdata/ and tests/; tests are counted but not capped. At 95 %
# this warns.
#
# Crossing the cap means raising it in the same PR, with its reason in
# scripts/loc.txt and the owner's approval. This file is laib's scaffold's
# (scripts/copies.sh); a repo that needs areas writes its own loc.sh and no
# loc.txt, as laib and homes do.
set -e
cd "$(dirname "$0")/.."
caps=$(sed 's/#.*//' scripts/loc.txt | grep -v '^[[:space:]]*$' || true)
[ "$(printf '%s\n' "$caps" | grep -c .)" -eq 1 ] || { echo "scripts/loc.txt: one line <cap> <folder…>, not: $caps"; exit 1; }
# shellcheck disable=SC2086
set -- $caps
cap=$1
shift
case $cap in '' | *[!0-9]*) echo "scripts/loc.txt: the cap is a number, not $cap"; exit 1 ;; esac
[ $# -gt 0 ] || { echo "scripts/loc.txt: no folder to count"; exit 1; }
for d in "$@"; do [ -e "$d" ] || { echo "scripts/loc.txt: $d does not exist"; exit 1; }; done
count() { # count prod|test
  find "$@" -name .git -prune -o -type f \( -name '*.go' -o -name '*.py' \) -print | awk -v want="$want" '
    { n = split($0, part, "/"); f = part[n]; t = (f ~ /_test\.go$/ || f ~ /^test_.*\.py$/ || f ~ /_test\.py$/)
      for (i = 1; i < n; i++) if (part[i] == "tests" || part[i] == "testdata") t = 1
      if ((want == "test") == t) print }' | while IFS= read -r f; do cat "$f"; done | wc -l
}
want=prod; prod=$(count "$@")
want=test; test=$(count "$@")
prod=$((prod + 0)) test=$((test + 0))
echo "production: $prod  tests: $test  (cap $cap: $*)"
[ "$prod" -lt "$cap" ] || { echo "over $cap lines: raise the cap in scripts/loc.txt in this PR, with its reason and the owner's yes"; exit 1; }
[ "$((prod * 100))" -lt "$((cap * 95))" ] || echo "warning: at $prod of $cap lines, 95 % or more"
