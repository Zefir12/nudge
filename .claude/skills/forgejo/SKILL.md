---
name: forgejo
description: How to work with issues and pull requests on git.zefirlabs.net using fj (forgejo-cli). Use whenever you read, create, comment on or label an issue, open or update a PR, or check CI status.
---

# Forgejo via fj (forgejo-cli 0.7)

The repo is `zefir/nudge` on https://git.zefirlabs.net. Run fj from the repo root;
it finds the repo and host from the `origin` remote. If that fails, add
`-H git.zefirlabs.net` (host) and `-r zefir/nudge` where the command takes `--repo`.

## Rules
- **Always pass a body** (`--body "..."` or `--body-file <file>`). Without one fj opens an
  editor and the session hangs. For anything longer than a line, write the body to a file
  in the scratchpad or /tmp and use `--body-file`.
- `fj issue create` needs `--no-template` when you pass your own body. Follow the
  structure of `.forgejo/issue_template/task.md` (Goal / Context / Acceptance criteria /
  Out of scope) in that body.
- Never run `fj auth ...`, `fj pr merge` or `fj pr close`. The hook blocks them.
- Never print or read fj's keys file.

## Sanity check
```sh
fj whoami            # must print gitek (the bot account); if not, stop and tell zefir
```

## Issues
```sh
fj issue search --labels claude:ready             # open issues ready for Claude
fj issue search "nflog" --state all               # text search
fj issue view 12                                  # title + body
fj issue view 12 comments                         # all comments
fj issue comment 12 --body-file /tmp/plan.md      # comment (or: fj issue comment 12 "short text")
fj issue edit 12 labels --add claude:in-progress --rm claude:ready
fj issue create "Evict idle IPs from detector" --no-template --body-file /tmp/issue.md
fj issue edit 12 labels --add idea                # label a new issue after creating it
fj issue edit 12 milestone "Phase 3"              # set the milestone after creating it (see below)
fj issue search --state all -m "Phase 3"          # issues in a milestone
fj issue close 12 --with-msg "Done in #15"        # only when zefir asked; PR "Closes #N" does it on merge
```

- `fj issue create --milestone <name>` is silently ignored in fj 0.7: the issue is
  created without a milestone and no error is shown. Create the issue, then set it with
  `fj issue edit N milestone "<name>"` and check with `fj issue search -m "<name>"`.
- `fj issue search -m <name>` with a milestone that doesn't exist ignores the filter and
  lists every issue. A real but empty milestone prints "0 issues".

## Pull requests
```sh
git push -u origin feat/12-evict-idle-ips
fj pr create "Evict idle IPs from detector" \
  --base develop --head feat/12-evict-idle-ips --body-file /tmp/pr.md
fj pr status 15            # mergeability + CI; add --wait to block until checks finish
fj pr view 15 comments     # review feedback to address
fj pr view 15 diff
fj pr comment 15 "Addressed review: ..."
fj pr edit 15 labels --add needs-human
fj pr search --state open
```
- Title prefix `WIP: ` makes a draft PR. Use it when you want early feedback.
- PR body follows `.forgejo/pull_request_template.md`, includes `Closes #N`, and ends with
  the attribution lines your session instructions specify.

## Full issue loop
1. `fj issue view N` and `fj issue view N comments | tail -n 60`. Unclear? Comment the questions,
   `--add needs-human --rm claude:ready`, stop.
2. `fj issue edit N labels --add claude:in-progress --rm claude:ready`; comment a short plan.
3. `git switch develop && git pull`, `git switch -c feat/N-short-name`: always a new
   branch for this issue, even if the session names another "designated" branch.
4. Implement with tests; `make test lint`; run the reviewer agent and fix its findings.
5. Push, `fj pr create ... --base develop` with `Closes #N`. Then mark merged work done:
   `fj issue search --state closed --labels claude:in-progress` lists issues closed by merged
   PRs; for each, `fj issue edit M labels --add claude:done --rm claude:in-progress`.
6. `fj pr status <pr> --wait`; if CI fails, fix and push again.
7. Review comments arrive: `fj pr view <pr> comments | tail -n 80`, fix, push, reply with `fj pr comment`.
8. Done: `git switch develop` and end the session. The next issue gets a fresh session.

## When fj can't do it
fj can't create labels or milestones, or reply inline to review threads. Say what you
need and ask zefir; don't call the API with credentials yourself.
