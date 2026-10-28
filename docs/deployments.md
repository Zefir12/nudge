# Deploying from CI

A deployment is a stack that CI deploys through. CI sets a few `.env` variables, such as
the image it just built, and nudge pulls and brings the stack up. With health checks on,
nudge also waits until the new version is actually up before calling the run a success.
The API reference is the Deployments section of [api.md](api.md).

## 1. Make the stack take the image from a variable

```yaml
services:
  api:
    image: ${API_IMAGE}
    healthcheck:
      test: ["CMD", "/app/healthcheck"]
      interval: 5s
      retries: 6
```

Create the stack on the environment, then a deployment on it (Deployments → New), with
`API_IMAGE` under "Variables deploys may set".

## 2. Choose when a run succeeds

Under the deployment's Settings → Health checks:

- **Wait for healthy:** compose waits this many seconds for every service to run and pass
  its healthcheck (`docker compose up --wait`). A service without a healthcheck only has
  to be running.
- **Stability window:** after that, every container must keep running for this many
  seconds without a restart. This catches a crash loop that starts after a few seconds.
  One-off containers that exit with 0 (migrations) are fine.
- **HTTP check:** a GET of `http://127.0.0.1:<port><path>` inside a running container of
  the service, which must answer the status (default 200; redirects aren't followed).
  The request comes from inside the environment, not from the nudge server's network, so
  no port has to be published. nudge starts a short-lived helper container from its own
  image in the service container's network namespace, so the service's image needs no
  curl.
- **Roll back when a check fails:** off by default. When on, a failed run deploys the
  values it replaced again (the update history records both). The run ends `rolled_back`,
  and CI still fails.
- **Remove finished one-off containers:** off by default. When on, a run that succeeded
  (after every check above) removes the stack's containers that exited with code 0 and
  have restart policy `no`: a `migrate` or `seed` service that ran before the server.
  Running containers, non-zero exits, restarting services and other stacks' containers
  are never touched, and a failed or rolled-back run removes nothing, so a failed
  migration stays to inspect. The next deploy creates them again, and `depends_on:
  condition: service_completed_successfully` still waits for them. Their logs go with
  them; the run's progress (and the server log) names the removed containers, and a
  removal that fails is a warning on the run, not a failure. The rule is exactly that: any
  service of the stack that is exited with 0 and has no restart policy once the run
  succeeded counts, whatever its name. It's a setting rather than a compose label because
  that rule already marks a one-off job (compose's own `oneoff` label is only set by
  `compose run`, not on a `migrate` service), and it keeps the compose file free of
  nudge-only labels.

A failed run without rollback puts the `.env` back, so the next start of the stack uses
the values that worked. The containers the failed run started keep running until then.

A run goes through `pulling → deploying → checking` and ends `succeeded`, `failed` or
`rolled_back`. A failure says which step and which service, never the daemon's own
message, which can carry secrets.

Agents built before a check skip it, and the run gets a warning. Update the agent to get
the check.

## 3. Add the CI step

Create a deploy token with the `deploy` scope (add `runs:read` to read runs) and store it
as the CI secret `NUDGE_DEPLOY_TOKEN`. The deployment page shows both steps below with
your server and variables filled in.

With curl, following the progress stream (Forgejo or GitHub Actions):

```yaml
- name: Deploy
  env:
    NUDGE_DEPLOY_TOKEN: ${{ secrets.NUDGE_DEPLOY_TOKEN }}
  run: |
    curl -fsSN -X POST \
      -H "Authorization: Bearer $NUDGE_DEPLOY_TOKEN" \
      -H "Content-Type: application/json" \
      -d '{"variables":{"API_IMAGE":"registry.example/api@${{ steps.build.outputs.digest }}"}}' \
      "https://nudge.example.net/api/v1/deployments/shop/deploys?stream=1" | tee progress.jsonl
    tail -n 1 progress.jsonl | grep -q '"status":"succeeded"'
```

With `nudge deploy`, the same binary as the server, which prints the progress and exits 0
only when the run succeeded:

```yaml
- name: Deploy
  env:
    NUDGE_DEPLOY_TOKEN: ${{ secrets.NUDGE_DEPLOY_TOKEN }}
  run: |
    docker run --rm -e NUDGE_DEPLOY_TOKEN registrydedic.zefirlabs.net/nudge:latest deploy \
      --server https://nudge.example.net --deployment shop \
      --var API_IMAGE=registry.example/api@${{ steps.build.outputs.digest }}
```

`--var` repeats for each variable. `--server` falls back to `NUDGE_SERVER_URL`. Exit codes:
0 succeeded, 1 failed, rolled back, superseded or no answer, 2 bad arguments.

Without streaming, `?wait=1` answers once the run ends: `200` succeeded, `502` failed or
rolled back, `409` superseded. `curl -f` turns those into a failing step too.

Pin images by digest (`image@sha256:…`) rather than a moving tag. A rollback then
deploys exactly the image that ran before.
