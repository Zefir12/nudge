<script setup lang="ts">
import { computed, ref } from 'vue';

import {
  createDeployHook,
  deleteDeployHook,
  listDeployHookRuns,
  listDeployHooks,
  rotateDeployHook,
  type DeployHook,
  type DeployHookRun,
  type DeployHookStatus,
  type DeployHookWithSecret,
} from '../api/client';
import { useLoad } from '../composables/useLoad';
import { formatDate, shortId } from '../format';
import LoadState from './LoadState.vue';

// WebhooksCard manages a container's or stack's deploy webhooks: URLs a CI job or a
// Forgejo webhook calls to pull the image and redeploy. A secret is shown only right after
// a create or rotate, so that panel stays until the user dismisses it. Pages key the card
// by its target, so moving to another container or stack starts afresh.
const props = defineProps<{ env: string; kind: 'container' | 'stack'; target: string }>();

const { data, error, reload } = useLoad(
  () => [props.env, props.kind, props.target] as const,
  ([env, kind, target]) => listDeployHooks(env, { kind, target }),
);

const form = ref({ name: '', tagFilter: '' });
const busy = ref(false);
const actionError = ref('');
// shown is the webhook whose secret was just created or rotated.
const shown = ref<DeployHookWithSecret | null>(null);
// runs holds the expanded webhooks' runs by id; null while they load.
const runs = ref<Record<string, DeployHookRun[] | null>>({});
const runsError = ref<Record<string, string>>({});

const message = (e: unknown) => (e instanceof Error ? e.message : String(e));

async function act(fn: () => Promise<void>) {
  busy.value = true;
  actionError.value = '';
  try {
    await fn();
  } catch (e) {
    actionError.value = message(e);
  } finally {
    busy.value = false;
  }
}

const onCreate = () =>
  act(async () => {
    shown.value = await createDeployHook(
      props.env,
      { kind: props.kind, target: props.target },
      { name: form.value.name.trim(), tag_filter: form.value.tagFilter.trim() },
    );
    form.value = { name: '', tagFilter: '' };
    await reload();
  });

function onRotate(h: DeployHook) {
  if (!window.confirm(`New secret for ${h.name}? The current one stops working at once.`)) return;
  return act(async () => {
    shown.value = await rotateDeployHook(props.env, h.id);
    await reload();
  });
}

function onDelete(h: DeployHook) {
  if (!window.confirm(`Delete the deploy webhook ${h.name}? Calls to its URL will fail.`)) return;
  return act(async () => {
    await deleteDeployHook(props.env, h.id);
    if (shown.value?.id === h.id) shown.value = null;
    delete runs.value[h.id];
    delete runsError.value[h.id];
    await reload();
  });
}

async function toggleRuns(h: DeployHook) {
  if (h.id in runs.value) {
    delete runs.value[h.id];
    return;
  }
  runs.value[h.id] = null;
  delete runsError.value[h.id];
  try {
    const list = await listDeployHookRuns(props.env, h.id);
    // Collapsed while loading: stay collapsed.
    if (runs.value[h.id] === null) runs.value[h.id] = list;
  } catch (e) {
    if (runs.value[h.id] !== null) return;
    delete runs.value[h.id];
    runsError.value[h.id] = message(e);
  }
}

const tone = (s: DeployHookStatus) => {
  switch (s) {
    case 'updated':
    case 'unchanged':
      return 'good';
    case 'failed':
      return 'bad';
    default:
      return 'warn';
  }
};

// images shortens a run's image IDs; a stack's are service=ID lists.
const images = (v: string) =>
  v
    .split(',')
    .filter(Boolean)
    .map((part) => {
      const i = part.indexOf('=');
      return i < 0 ? shortId(part) : `${part.slice(0, i)}=${shortId(part.slice(i + 1))}`;
    })
    .join(', ');

// The snippets are built here: "${{" in the template would start an interpolation.
const actionStep = computed(() =>
  shown.value
    ? `- name: Deploy\n  run: curl -fsS -X POST -H "Authorization: Bearer \${{ secrets.NUDGE_HOOK }}" "${shown.value.url}?wait=1"\n`
    : '',
);

type Copyable = 'url' | 'secret' | 'step';
const copied = ref<Copyable | ''>('');
const copyFailed = ref(false);

async function onCopy(which: Copyable, text: string) {
  copied.value = '';
  copyFailed.value = false;
  try {
    await navigator.clipboard.writeText(text);
    copied.value = which;
  } catch {
    // No clipboard API (plain HTTP): the values stay selectable.
    copyFailed.value = true;
  }
}

function onDone() {
  shown.value = null;
  copied.value = '';
  copyFailed.value = false;
}
</script>

<template>
  <section class="card">
    <h2>Deploy webhooks</h2>
    <p class="muted">
      A URL a CI job or a Forgejo webhook calls to pull this {{ kind }}'s image and redeploy it. Its secret
      can do nothing else.
    </p>

    <section v-if="shown" class="created" aria-live="polite">
      <h3>Secret for "{{ shown.name }}"</h3>
      <p>Copy the secret now: it is shown only once.</p>
      <dl>
        <dt>URL</dt>
        <dd>
          <code class="value url">{{ shown.url }}</code>
          <button type="button" @click="onCopy('url', shown.url)">
            {{ copied === 'url' ? 'Copied' : 'Copy URL' }}
          </button>
        </dd>
        <dt>Secret</dt>
        <dd>
          <code class="value secret">{{ shown.secret }}</code>
          <button type="button" @click="onCopy('secret', shown.secret)">
            {{ copied === 'secret' ? 'Copied' : 'Copy secret' }}
          </button>
        </dd>
      </dl>

      <h4>Forgejo Actions step</h4>
      <p>
        Store the secret in the repository's secrets as <code>NUDGE_HOOK</code> and add this step after the
        image is pushed. It waits for the deploy and fails the job if it fails.
      </p>
      <pre class="snippet">{{ actionStep }}</pre>
      <button type="button" @click="onCopy('step', actionStep)">
        {{ copied === 'step' ? 'Copied' : 'Copy step' }}
      </button>

      <h4>Forgejo webhook</h4>
      <p>In the repository's or organisation's settings, add a Forgejo webhook with:</p>
      <ul class="native">
        <li>Target URL: the URL above</li>
        <li>HTTP method: <code>POST</code>, content type: <code>application/json</code></li>
        <li>Secret: the secret above</li>
        <li>Trigger on: custom events, <strong>Package</strong></li>
      </ul>
      <p class="muted">
        A webhook can't wait for the deploy: the runs below show how it went. Set a tag filter so other tags
        of the package are skipped.
      </p>

      <p v-if="copyFailed" class="muted">Copying is not available here: select the text and copy it.</p>
      <div class="buttons">
        <button type="button" @click="onDone">Done</button>
      </div>
    </section>

    <form class="create" @submit.prevent="onCreate">
      <label>
        Name
        <input v-model="form.name" name="name" maxlength="64" placeholder="e.g. ci" required />
      </label>
      <label>
        Tag filter
        <input v-model="form.tagFilter" name="tag_filter" placeholder="optional, e.g. 1.*" />
      </label>
      <button type="submit" class="primary" :disabled="busy">Add</button>
    </form>
    <p v-if="actionError" class="error" role="alert">{{ actionError }}</p>

    <LoadState
      :loading="false"
      :error="error"
      :ready="data !== null"
      :empty="data?.length === 0"
      empty-text="No deploy webhooks yet."
      @retry="reload"
    >
      <div class="table-wrap">
        <table>
          <thead>
            <tr>
              <th>Name</th>
              <th>Tag filter</th>
              <th>Last run</th>
              <th class="wide-only">Last triggered</th>
              <th></th>
            </tr>
          </thead>
          <tbody>
            <template v-for="h in data" :key="h.id">
              <tr>
                <td>
                  {{ h.name }}
                  <span class="muted"> by {{ h.created_by }}</span>
                </td>
                <td>
                  <code v-if="h.tag_filter">{{ h.tag_filter }}</code>
                  <span v-else class="muted">any</span>
                </td>
                <td>
                  <span v-if="h.last_status" class="badge" :class="tone(h.last_status)">{{
                    h.last_status
                  }}</span>
                  <span v-else class="muted">none</span>
                </td>
                <td class="wide-only">
                  {{ h.last_triggered_at ? formatDate(h.last_triggered_at) : 'never' }}
                </td>
                <td class="row-actions">
                  <button
                    type="button"
                    :aria-expanded="h.id in runs || !!runsError[h.id]"
                    @click="toggleRuns(h)"
                  >
                    Runs
                  </button>
                  <button type="button" :disabled="busy" @click="onRotate(h)">New secret</button>
                  <button type="button" class="danger" :disabled="busy" @click="onDelete(h)">Delete</button>
                </td>
              </tr>
              <tr v-if="h.id in runs || runsError[h.id]" class="runs">
                <td colspan="5">
                  <p v-if="runsError[h.id]" class="error" role="alert">{{ runsError[h.id] }}</p>
                  <p v-else-if="runs[h.id] === null" class="muted">Loading…</p>
                  <p v-else-if="runs[h.id]?.length === 0" class="muted">No runs yet.</p>
                  <table v-else>
                    <thead>
                      <tr>
                        <th>Status</th>
                        <th>Time</th>
                        <th>Image</th>
                        <th>Error</th>
                      </tr>
                    </thead>
                    <tbody>
                      <tr v-for="r in runs[h.id]" :key="r.id">
                        <td>
                          <span class="badge" :class="tone(r.status)">{{ r.status }}</span>
                        </td>
                        <td>{{ formatDate(r.queued_at) }}</td>
                        <td class="mono break">
                          <template v-if="r.image_before || r.image_after">
                            {{ images(r.image_before) || '–' }} → {{ images(r.image_after) || '–' }}
                          </template>
                        </td>
                        <td class="error">{{ r.error }}</td>
                      </tr>
                    </tbody>
                  </table>
                </td>
              </tr>
            </template>
          </tbody>
        </table>
      </div>
    </LoadState>
  </section>
</template>

<style scoped>
.created {
  margin-bottom: 1rem;
  padding: 0.75rem 1rem;
  border: 1px solid var(--accent);
  border-radius: var(--radius);
}

.created h3 {
  margin-top: 0;
}

.created dl {
  display: grid;
  grid-template-columns: auto 1fr;
  gap: 0.5rem 0.75rem;
  align-items: center;
}

.created dd {
  display: flex;
  gap: 0.5rem;
  align-items: center;
  margin: 0;
  min-width: 0;
}

.value,
.snippet {
  padding: 0.25rem 0.5rem;
  background: var(--bg);
  border-radius: var(--radius);
  user-select: all;
}

.value {
  word-break: break-all;
}

.snippet {
  margin: 0 0 0.5rem;
  padding: 0.5rem 0.75rem;
  overflow-x: auto;
}

.native {
  margin: 0;
  padding-left: 1.25rem;
}

.create {
  display: flex;
  align-items: flex-end;
  gap: 0.75rem;
  flex-wrap: wrap;
  margin-bottom: 1rem;
}

.create label {
  display: flex;
  flex-direction: column;
  gap: 0.25rem;
  font-weight: 500;
}

.buttons,
.row-actions {
  display: flex;
  gap: 0.5rem;
  margin-top: 0.75rem;
}

.row-actions {
  margin-top: 0;
}

.badge {
  display: inline-block;
  padding: 0.05rem 0.5rem;
  font-size: 0.85em;
  white-space: nowrap;
  border: 1px solid currentColor;
  border-radius: 999px;
}

.good {
  color: var(--ok);
}

.warn {
  color: var(--warn);
}

.bad {
  color: var(--danger);
}

.mono {
  font-family: var(--mono);
  font-size: 0.9em;
}

.runs td p {
  margin: 0;
}
</style>
