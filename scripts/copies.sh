#!/usr/bin/env bash
# The gates have one source (laib#1170): laib's werkstatt/scaffold/repo/scripts/.
# Every repo of the household (laib, homes, archiv, dokumente, and each repo
# `laib-app new` makes) carries copies of ratchet.py, test_ratchet.py,
# pins.sh and this file, and loc.sh where scripts/loc.txt holds its one cap
# (laib and homes keep a loc.sh of their own, with areas). This fails when a
# copy differs from the source: change laib's first, then copy it here.
#
# In laib the source is the tree itself; elsewhere laib's main on GitHub.
# COPIES_FROM names another source (a folder or a URL). Usage:
# scripts/copies.sh [repo dir]
set -euo pipefail
cd "${1:-$(dirname "$0")/..}"
here=werkstatt/scaffold/repo/scripts
src=${COPIES_FROM:-}
if [ -z "$src" ]; then
  if [ -d "$here" ]; then src=$here; else src=https://raw.githubusercontent.com/mdopp/laib/main/$here; fi
fi
files="copies.sh pins.sh ratchet.py test_ratchet.py"
[ ! -f scripts/loc.txt ] || files="$files loc.sh"
want=$(mktemp)
trap 'rm -f "$want"' EXIT
bad=0
for f in $files; do
  case $src in
    http://* | https://*) curl -fsSL -o "$want" "$src/$f" || { echo "copies: cannot read $src/$f"; exit 2; } ;;
    *) cp "$src/$f" "$want" ;;
  esac
  if [ ! -f "scripts/$f" ]; then
    echo "scripts/$f is missing: copy it from $src/$f"; bad=$((bad + 1))
  elif ! cmp -s "$want" "scripts/$f"; then
    echo "scripts/$f differs from $src/$f:"; diff -u "$want" "scripts/$f" | head -20 || true; bad=$((bad + 1))
  fi
done
echo "copies: $bad of the gates' files differ from their source ($src)"
[ "$bad" -eq 0 ]
