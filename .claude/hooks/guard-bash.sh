#!/bin/sh
# PreToolUse hook for Bash: blocks commands CLAUDE.md forbids, including variants that
# permission prefix rules miss (`git push origin HEAD:main`, `cd x && sudo ...`).
# Only matches at command positions (start, or after ; & | ( ), so commit messages
# that merely mention sudo, docker rm or main are fine. Exit 2 = block; stderr goes to Claude.
input=$(cat | tr '\n' ' ')
START='("command"[[:space:]]*:[[:space:]]*"|[;&|(][[:space:]]*)'

block() { echo "Blocked by .claude/hooks/guard-bash.sh: $1" >&2; exit 2; }
has() { printf '%s' "$input" | grep -Eq "$START$1"; }

has '(sudo|doas)[[:space:]]' && block "no sudo"
has 'fj([[:space:]]+-[^[:space:]]+[[:space:]]+[^[:space:]]+)*[[:space:]]+pr[[:space:]]+(merge|close)' && block "zefir merges and closes PRs, not Claude"
has 'fj([[:space:]]+-[^[:space:]]+[[:space:]]+[^[:space:]]+)*[[:space:]]+auth' && block "fj login is managed by zefir"
has 'make[[:space:]]+(publish|push)' && block "publishing images is zefir's job"
has 'docker[[:space:]]+(push|buildx[^;&|"]*--push)' && block "publishing images is zefir's job"
# The host's Docker is shared with real workloads: never change its state.
has 'docker([[:space:]]+(container|image|volume|network|system))?[[:space:]]+(rm|rmi|stop|kill|prune|restart|pause|down)([[:space:]"]|$)' \
  && block "never change the host's containers, images, volumes or networks"
has 'docker[[:space:]]+compose[^;&|"]*[[:space:]](down|rm|stop|kill)([[:space:]"]|$)' \
  && block "never change the host's containers, images, volumes or networks"

# Every `git ... push ...` segment up to the next command separator.
pushes=$(printf '%s' "$input" | grep -Eo "${START}git([[:space:]]+-C[[:space:]]+[^[:space:]]+)?[[:space:]]+push[^;&|\"]*")
if [ -n "$pushes" ]; then
  printf '%s\n' "$pushes" | grep -Eq '[[:space:]](-f|--force|--force-with-lease[^[:space:]]*|--mirror|--delete|-d)([[:space:]]|$)|[[:space:]]\+' \
    && block "force/delete/mirror pushes are not allowed"
  printf '%s\n' "$pushes" | grep -Eq '[[:space:]:]main([[:space:]]|$)|refs/heads/main' \
    && block "never push to main; push a branch and open a PR to develop"
fi
exit 0
