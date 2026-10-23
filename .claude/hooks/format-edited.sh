#!/bin/sh
# PostToolUse hook for Edit/Write: gofmt the edited .go file if gofmt is on the host.
# Silently does nothing otherwise (make lint still catches formatting, and `make format`
# fixes the frontend).
command -v gofmt >/dev/null 2>&1 || exit 0
file=$(cat | grep -o '"file_path"[[:space:]]*:[[:space:]]*"[^"]*"' | head -1 | sed 's/.*"\([^"]*\)"$/\1/')
case "$file" in
  *.go) [ -f "$file" ] && gofmt -w "$file" ;;
esac
exit 0
