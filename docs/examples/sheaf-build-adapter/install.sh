#!/usr/bin/env bash
# Install the sheaf-build-adapter skill for Claude Code.
#
# Copies SKILL.md + PROCEDURE.md + README.md into
# ~/.claude/skills/sheaf-build-adapter/ so the skill is available in every
# Claude Code conversation, regardless of which directory the session is
# rooted in.
#
# Idempotent — re-run after editing any source file to refresh the install.
#
# Optional flags:
#   --dry-run       show actions, don't execute
#   --dest PATH     override install dir (default: ~/.claude/skills/sheaf-build-adapter)
#   --uninstall     remove the installed skill instead of installing it

set -euo pipefail

SRC_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DEST_DIR="${HOME}/.claude/skills/sheaf-build-adapter"
DRY_RUN=0
UNINSTALL=0

while [[ $# -gt 0 ]]; do
  case "$1" in
    --dry-run) DRY_RUN=1; shift ;;
    --dest) DEST_DIR="$2"; shift 2 ;;
    --uninstall) UNINSTALL=1; shift ;;
    -h|--help)
      sed -n '2,/^set -euo pipefail$/p' "${BASH_SOURCE[0]}" | sed -e 's/^# \{0,1\}//' -e '$d'
      exit 0 ;;
    *)
      echo "unknown arg: $1" >&2
      exit 2 ;;
  esac
done

run() {
  if [[ "$DRY_RUN" -eq 1 ]]; then
    printf "[dry-run] %s\n" "$*"
  else
    eval "$@"
  fi
}

if [[ "$UNINSTALL" -eq 1 ]]; then
  if [[ -d "$DEST_DIR" ]]; then
    run "rm -rf '$DEST_DIR'"
    echo "removed $DEST_DIR"
  else
    echo "nothing to remove: $DEST_DIR does not exist"
  fi
  exit 0
fi

for f in SKILL.md PROCEDURE.md README.md; do
  if [[ ! -f "$SRC_DIR/$f" ]]; then
    echo "error: missing source file $SRC_DIR/$f" >&2
    exit 3
  fi
done

run "mkdir -p '$DEST_DIR'"
for f in SKILL.md PROCEDURE.md README.md; do
  run "cp '$SRC_DIR/$f' '$DEST_DIR/$f'"
done

cat <<EOF

installed sheaf-build-adapter → $DEST_DIR

Start a new Claude Code conversation and invoke with:

    /sheaf-build-adapter      (or: "build a sheaf adapter for <format>")

The skill is autoloaded by name. It is usually reached as a handoff from
sheaf-onboard when config can't close a coverage gap. Re-run this script
after editing the source files to refresh the install.
EOF
