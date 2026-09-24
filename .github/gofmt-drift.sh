#!/usr/bin/env bash
# gofmt-drift.sh — fail only if a change ADDS gofmt drift.
#
# The tree carries pre-existing formatting deviations inherited from upstream
# (struct field alignment, mostly). Reformatting them wholesale would conflict
# with every future upstream merge, so the rule cannot be "changed files must be
# clean" — that punishes you for touching an already-dirty file and is exactly
# the trap this repo kept falling into. The rule is "your change must not add
# deviation".
#
# Method: for each changed .go file, take gofmt's diff at the base revision and
# at HEAD, normalize away whitespace and line numbers, and compare the SETS of
# lines gofmt wants to rewrite. Equal means you introduced none.
#
# One definition, used by CI and by the deploy gate, because two gates with two
# different notions of "pass" is how a green local run turns red on push.
#
# Usage: .github/gofmt-drift.sh <base-ref>
set -uo pipefail

BASE="${1:?usage: gofmt-drift.sh <base-ref>}"
command -v gofmt >/dev/null || { echo "gofmt not on PATH" >&2; exit 2; }

changed=$(git diff --name-only "$BASE" HEAD -- '*.go')
if [ -z "$changed" ]; then
  echo "gofmt: no .go changes vs $BASE"
  exit 0
fi

WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT

while IFS= read -r f; do
  [ -n "$f" ] || continue
  mkdir -p "$WORK/new/$(dirname "$f")" "$WORK/old/$(dirname "$f")"
  [ -f "$f" ] && cp "$f" "$WORK/new/$f"
  git show "$BASE:$f" 2>/dev/null > "$WORK/old/$f"
  # A file that does not exist at the base has no inherited drift to compare.
  [ -s "$WORK/old/$f" ] || rm -f "$WORK/old/$f"
done <<< "$changed"

raw=$(cd "$WORK" && gofmt -d old new 2>/dev/null)

newdrift=$(printf '%s\n' "$raw" | python3 -c '
import collections, re, sys

drift = collections.defaultdict(list)
side = None
# gofmt -d emits: "diff -u old/path/x.go.orig old/path/x.go" -- two paths on the
# line, so anchor on the FIRST and ignore the rest.
header = re.compile(r"^diff\s+(?:-u\s+)?(?:\./)?(old|new)/(.+?)\.orig\s")
for line in sys.stdin.read().splitlines():
    m = header.match(line)
    if m:
        side = (m.group(1), m.group(2))
        continue
    if side and re.match(r"^[+-]", line) and not re.match(r"^(\+\+\+|---)", line):
        # Whitespace and line numbers both shift as code is added above; what
        # matters is WHICH lines gofmt rewrites, not where they sit.
        drift[side].append("".join(line.split()))

# Only lines gofmt would rewrite in the new file and not in the old one count.
# Comparing the two sets for equality also failed a change that REMOVED drift.
for f in sorted({name for _, name in drift}):
    added = collections.Counter(drift.get(("new", f), [])) - collections.Counter(drift.get(("old", f), []))
    if added:
        print(f)
')

count=$(printf '%s\n' "$changed" | grep -c .)
inherited=$(printf '%s\n' "$raw" | grep -cE '^diff ' || true)

if [ -n "$newdrift" ]; then
  echo "gofmt: this change ADDED formatting drift in:"
  printf '  %s\n' $newdrift
  echo
  echo "run: gofmt -w $(printf '%s ' $newdrift)"
  exit 1
fi

echo "gofmt: no new drift ($count changed, $inherited pre-existing deviation(s) tolerated)"
