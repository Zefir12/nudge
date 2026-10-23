# Claude Code setup (zefir's one-time checklist)

Claude works on issues labelled `claude:ready`, one per session, on branches, and opens PRs
into `develop`. zefir reviews and merges every PR. Claude must not do any of the steps
below itself.

Already in place on this machine: the bot account `gitek` (fj is signed in as it), git's
identity `agent <agent@zefirlabs.net>` and HTTPS push credentials (git credential store),
Docker, make, fj. The Forgejo runners (`runs-on: docker`) are shared with go-guarding-dog.

## Forge
1. **Bot access.** Give `gitek` **Write** (not Admin) on `zefir/nudge`.
2. **Branches.** `develop` is pushed. Create `main` from it, and make `develop` the default branch.
   - Protect `main`: push only by zefir, 1 approval, required checks
     (`ci / test`, `ci / lint`, `ci / web`), no force push.
   - Protect `develop`: zefir and `gitek` may push, no force push, required checks for
     PR merges.
3. **Units and labels.** Enable Issues, PRs and Actions; check a runner serves the repo.
   Create the labels `claude:ready`, `claude:in-progress`, `claude:done`, `needs-human`,
   `idea`, `task` and `bug`.
4. **Registry secrets.** Add the repo Actions secrets `REGISTRY_USER` and
   `REGISTRY_PASSWORD` for `registrydedic.zefirlabs.net` (image.yaml pushes
   `nudge:develop` and `nudge:develop-<sha>` on every push to develop).
5. **Milestones** (optional): one per PLAN.md phase, so issues can be grouped.

## First run
Create issues from PLAN.md phase 1 (or ask Claude to draft them as issues, unlabelled,
for you to label), label the first one `claude:ready`, and start Claude Code in the repo
with "pick up the next claude:ready issue". You should see the label switch, a plan
comment, a `feat/<n>-…` branch and a PR into `develop` with green CI.

## Verify the guard hook
```sh
for c in 'git push origin HEAD:main' 'cd x && sudo ls' 'git push -f origin feat/1' \
         'fj pr merge 3' 'docker rm -f web' 'git commit -m "mention main, sudo and docker rm"'; do
  printf '{"tool_input":{"command":"%s"}}' "$(printf '%s' "$c" | sed 's/"/\\"/g')" \
    | .claude/hooks/guard-bash.sh 2>/dev/null; echo "$? $c"
done   # 2 for all but the commit, which prints 0
```
