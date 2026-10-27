---
name: forgejo
description: How to work with issues and pull requests on git.zefirlabs.net using fj (the Go forgejo CLI, gh-style). Use whenever you read, create, comment on or label an issue, open or update a PR, or check CI status.
---

# Forgejo via fj (0.6, gh-style flags)

The repo is `zefir/nudge` on https://git.zefirlabs.net. Run fj from the repo root; it
finds the repo from the `origin` remote (else add `-R zefir/nudge`). git talks HTTPS
(SSH is not reachable through the Cloudflare proxy); credentials are already stored.
Most commands take `--json <fields> --jq <expr>` for compact output; use them instead of
dumping long views.

## Rules
- **Always pass a body** (`--body "..."` or `--body-file <file>`, `-` for stdin). For
  anything longer than a line, write it to a file in the scratchpad or /tmp.
- Issue bodies follow `.forgejo/issue_template/task.md` (Goal / Context / Acceptance
  criteria / Out of scope).
- Never run `fj auth ...` (`fj auth token` prints the secret), `fj pr merge` or
  `fj pr close`. The hook blocks them. `fj api` is for reading CI status only.

## Sanity check
    fj version                   # must be 0.6.x (Go, gh-style)
    fj api user --jq .login      # must print klodzik; if not, stop and tell zefir

This is the Go `fj` 0.6 with gh-style commands. Some machines may still have the older
Rust forgejo-cli 0.7, also called `fj`: there, `fj version` and `fj api` are unknown
commands, and it has `fj whoami` and `fj issue search`. If you find that one, don't work
around it with the old syntax. Stop and ask zefir to install the Go fj 0.6 on that
machine (logged in as klodzik, config in `~/.config/fj/config.yaml`). The gh-style
`--json`/`--jq` output is why: it saves tokens on every issue and PR read.

## Issues
    fj issue list --label claude:ready                          # open issues ready for Claude
    fj issue list --search "text" --state all
    fj issue view 12                                            # title + body
    fj issue view 12 --comments                                 # with comments
    fj issue comment create 12 --body-file /tmp/plan.md
    fj issue edit 12 --add-label claude:in-progress --remove-label claude:ready
    fj issue create --title "Title" --body-file /tmp/issue.md --label task
    fj issue create --title "Idea: ..." --body-file /tmp/idea.md --label idea
    fj issue edit 12 --milestone "Phase 1"
    fj issue list --state all --milestone "Phase 1"

## Pull requests
    git push -u origin feat/12-short-name
    fj pr create --title "Title" --base develop --head feat/12-short-name --body-file /tmp/pr.md
    fj pr view 15 --json title,state,mergeable,headRefName
    fj pr view 15 --comments                 # conversation
    fj pr review list 15                     # reviews; `fj pr review view <id> 15` for inline comments
    fj pr diff 15
    fj pr comment create 15 --body "Addressed review: ..."
    fj pr edit 15 --add-label needs-human
- `--draft` makes a draft PR.
- The PR body follows `.forgejo/pull_request_template.md`, includes `Closes #N` and ends
  with the attribution lines from your session instructions.

## CI status
    fj api "repos/{owner}/{repo}/commits/$(git rev-parse HEAD)/status" \
      --jq '.state, (.statuses[] | "\(.status)  \(.context)")'
Use the commit SHA: branch names with a slash (`feat/12-x`) return 404 here.
`state` is pending/success/failure. Job logs are not readable with the token: reproduce
the failure locally with the matching `make test-<x>` / `make lint-<x>`, or ask zefir to
paste the log.

## Full issue loop
1. `fj issue view N --comments`. If it's unclear, comment your questions,
   `fj issue edit N --add-label needs-human --remove-label claude:ready`, and stop.
2. `fj issue edit N --add-label claude:in-progress --remove-label claude:ready`; comment a short plan.
3. `git switch develop && git pull`, `git switch -c feat/N-short-name`: always a new branch.
4. Implement with tests; `make test lint`; run the reviewer agent and fix its findings.
5. Push, `fj pr create ... --base develop` with `Closes #N`. Then mark merged work done:
   `fj issue list --state closed --label claude:in-progress --json number --jq '.[].number'`,
   and for each `fj issue edit M --add-label claude:done --remove-label claude:in-progress`.
6. Check CI on the branch (above); if it fails, fix and push again.
7. Review comments: `fj pr view <pr> --comments` and `fj pr review list <pr>`, fix, push,
   reply with `fj pr comment create`.
8. Done: `git switch develop` and end the session.

## When fj can't do it
Ask zefir (labels, milestones, protections, logs). Don't call the API with raw
credentials yourself.
