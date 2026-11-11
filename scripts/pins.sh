#!/usr/bin/env bash
# What builds an image is pinned as what runs it (laib ADR 0034, laib#1165):
# every `uses:` in a workflow or action names a 40-hex commit, its tag kept
# as a comment (`uses: actions/checkout@<sha> # v4.4.0`), and every `FROM`
# in a Containerfile or Dockerfile names its image with `@sha256:` (`scratch`
# and a stage named earlier in the same file are exempt). A moved tag would
# otherwise change what gets built and signed. The pins move by pull request:
# Dependabot, weekly and grouped (.github/dependabot.yml).
#
# The same file runs in every repo of the household (laib, homes,
# homes-android, archiv, dokumente, 2ndbrain, and each repo `laib-app new`
# makes); copy a fix to all of them. Usage: scripts/pins.sh [repo dir]
set -euo pipefail
cd "${1:-$(dirname "$0")/..}"

files() { # $1: find predicates; skips .git and node_modules
  find . \( -name .git -o -name node_modules \) -prune -o -type f \( "$@" \) -print | sort
}

bad=0
# no {40} in a regex: mawk, the runner's awk, may lack interval expressions
hex='function hexend(s, n,  t) { t = substr(s, length(s) - n); return length(s) > n && substr(t, 1, 1) ~ /[@:]/ && substr(t, 2) !~ /[^0-9a-f]/ }'
while IFS= read -r f; do
  out=$(awk "$hex"'
    { sub(/\r$/, "") }
    /^[[:space:]]*(-[[:space:]]+)?uses:/ {
      ref = $0
      sub(/^[[:space:]]*(-[[:space:]]+)?uses:[[:space:]]*/, "", ref)
      sub(/[[:space:]]+#.*$/, "", ref); gsub(/["'\''[:space:]]/, "", ref)
      if (ref ~ /^\.\//) next                                  # an action of this repo
      if (ref ~ /^docker:\/\//) { if (ref !~ /@sha256:/ || !hexend(ref, 64)) print FNR ": " ref " (an image without @sha256:)"; next }
      if (!hexend(ref, 40) || ref ~ /@sha256:/) print FNR ": " ref " (a tag, not a 40-hex commit)"
    }' "$f")
  [ -z "$out" ] || { bad=$((bad + $(wc -l <<<"$out"))); sed "s|^|$f:|" <<<"$out"; }
done < <(files -path '*/.github/workflows/*.yml' -o -path '*/.github/workflows/*.yaml' -o -name action.yml -o -name action.yaml)

while IFS= read -r f; do
  out=$(awk "$hex"'
    { sub(/\r$/, "") }
    toupper($1) == "FROM" {
      i = 2; while ($i ~ /^--/) i++                            # --platform=…
      img = $i
      if (toupper($(i + 1)) == "AS") stage[tolower($(i + 2))] = 1
      if (img == "scratch" || (tolower(img) in stage && img !~ /[\/:@]/)) next
      if (img !~ /@sha256:/ || !hexend(img, 64)) print FNR ": " img " (no @sha256: digest)"
    }' "$f")
  [ -z "$out" ] || { bad=$((bad + $(wc -l <<<"$out"))); sed "s|^|$f:|" <<<"$out"; }
done < <(files -name 'Containerfile*' -o -name 'Dockerfile*' -o -name '*.Containerfile' -o -name '*.Dockerfile')

echo "pins: $bad action or base image without a commit or a digest (laib ADR 0034)"
[ "$bad" -eq 0 ]
