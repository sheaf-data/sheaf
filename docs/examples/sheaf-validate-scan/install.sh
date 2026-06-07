#!/usr/bin/env bash
# Install the sheaf-validate-scan skill globally for Claude Code.
#
# Copies SKILL.md + validate.py + README.md into
# ~/.claude/skills/sheaf-validate-scan/ so the skill is available in
# every Claude Code conversation, regardless of which directory the
# session is rooted in.
#
# Idempotent — re-run after editing any of the source files in this
# directory to update the installed copy. Pass --dry-run to print what
# would happen without touching disk.
#
# Optional flags:
#   --dry-run       show actions, don't execute
#   --dest PATH     override install dir (default: ~/.claude/skills/sheaf-validate-scan)
#   --uninstall     remove the installed skill instead of installing it

set -euo pipefail

SRC_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DEST_DIR="${HOME}/.claude/skills/sheaf-validate-scan"
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

# Sanity-check sources exist.
for f in SKILL.md validate.py README.md; do
  if [[ ! -f "$SRC_DIR/$f" ]]; then
    echo "error: missing source file $SRC_DIR/$f" >&2
    exit 3
  fi
done

run "mkdir -p '$DEST_DIR'"
for f in SKILL.md validate.py README.md; do
  run "cp '$SRC_DIR/$f' '$DEST_DIR/$f'"
done
run "chmod +x '$DEST_DIR/validate.py'"

# Sanity-check validate.py parses with the host's python3 — catches a
# stale install where someone edited validate.py and broke its syntax.
if [[ "$DRY_RUN" -eq 0 ]]; then
  if ! python3 -c "import ast; ast.parse(open('$DEST_DIR/validate.py').read())" 2>/dev/null; then
    echo "warning: installed validate.py has a Python syntax error" >&2
  fi
fi

cat <<EOF

installed sheaf-validate-scan → $DEST_DIR

Start a new Claude Code conversation and invoke with:

    /sheaf-validate-scan --config <path/to/sheaf.textproto> --repo <path/to/repo>

The skill is autoloaded by name; no shell or config changes needed.
Re-run this script after editing the source files to refresh the install.
EOF
