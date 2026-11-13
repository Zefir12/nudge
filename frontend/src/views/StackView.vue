<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, ref, shallowRef, watch } from 'vue';
import { onBeforeRouteLeave, RouterLink, useRouter } from 'vue-router';

import {
  downStack,
  emptyStackGit,
  getStack,
  getStackEnvExample,
  pullStackGit,
  removeStack,
  removeStackGit,
  setStackGit,
  updateStack,
  updateStackImages,
  upStack,
  type Stack,
  type StackGitInput,
  type StackLine,
} from '../api/client';
import FoldCard from '../components/FoldCard.vue';
import LoadState from '../components/LoadState.vue';
import StackEditor from '../components/StackEditor.vue';
import StackGitForm from '../components/StackGitForm.vue';
import StackLogs from '../components/StackLogs.vue';
import StateBadge from '../components/StateBadge.vue';
import UpdateHistory from '../components/UpdateHistory.vue';
import WebhooksCard from '../components/WebhooksCard.vue';
import { readCompose } from '../compose';
import { useLiveReload } from '../composables/useEvents';
import { useLoad } from '../composables/useLoad';
import { useParams } from '../composables/params';

const { env, id } = useParams();
const router = useRouter();
const {
  data: loaded,
  error,
  reload,
} = useLoad(
  () => [env.value, id.value] as const,
  ([e, n]) => getStack(e, n),
);
// Container events (a deploy, a crash, changes made elsewhere) refresh the services.
useLiveReload(env, ['container'], reload);

// stack is the last stack loaded: a live reload that fails shows an error but keeps the
// page (and its log streams) instead of unmounting it.
const stack = shallowRef<Stack | null>(null);
watch(loaded, (s) => {
  if (s) stack.value = s;
});
watch([env, id], () => (stack.value = null));

// The editor's drafts follow the saved files until the user changes them.
const compose = ref('');
const envFile = ref('');
const saved = ref<{ compose: string; env: string } | null>(null);
const dirty = computed(
  () => saved.value !== null && (compose.value !== saved.value.compose || envFile.value !== saved.value.env),
);
watch(stack, (s) => {
  if (!s || dirty.value) return;
  compose.value = s.compose;
  envFile.value = s.env;
  saved.value = { compose: s.compose, env: s.env };
});
watch([env, id], () => (saved.value = null));
onBeforeRouteLeave(() => !dirty.value || window.confirm('Leave without saving your changes to the files?'));

// rows lists every service: the ones the saved file declares, then any others that have
// containers (removed from the file but still running), each with its containers.
const rows = computed(() => {
  const s = stack.value;
  if (!s) return [];
  const byName = new Map(s.services.map((svc) => [svc.name, svc.containers]));
  const names = [...new Set([...readCompose(s.compose).services, ...byName.keys()])];
  return names.map((name) => ({ name, containers: byName.get(name) ?? [] }));
});
const logSources = computed(() =>
  (stack.value?.services ?? []).flatMap((svc) =>
    svc.containers.map((c) => ({ id: c.id, name: c.name, service: svc.name })),
  ),
);

const busy = ref(false);
const actionError = ref('');
const output = ref<StackLine[]>([]);
const outcome = ref('');
const outputBox = ref<HTMLElement | null>(null);
const volumes = ref(false);
let abort: AbortController | null = null;
onBeforeUnmount(() => abort?.abort());

async function run(
  label: string,
  start: (onLine: (l: StackLine) => void, signal: AbortSignal) => Promise<number>,
) {
  busy.value = true;
  actionError.value = '';
  output.value = [];
  outcome.value = `${label}…`;
  const controller = new AbortController();
  abort = controller;
  try {
    const code = await start((l) => {
      output.value.push(l);
      void nextTick(() => {
        if (outputBox.value) outputBox.value.scrollTop = outputBox.value.scrollHeight;
      });
    }, controller.signal);
    outcome.value = code === 0 ? `${label} finished.` : `${label} failed: compose exited with code ${code}.`;
  } catch (e) {
    if (controller.signal.aborted) return;
    outcome.value = '';
    actionError.value = e instanceof Error ? e.message : String(e);
  } finally {
    abort = null;
    busy.value = false;
    void reload();
  }
}

const history = ref<InstanceType<typeof UpdateHistory> | null>(null);
const onDeploy = () => run('Deploy', (onLine, signal) => upStack(env.value, id.value, false, onLine, signal));
async function onUpdate() {
  await run('Update', (onLine, signal) => updateStackImages(env.value, id.value, onLine, signal));
  void history.value?.reload();
}

function onDown() {
  const what = volumes.value
    ? 'its containers, networks and volumes (their data is lost)'
    : 'its containers and networks';
  if (!window.confirm(`Take "${id.value}" down? This removes ${what}.`)) return;
  void run('Down', (onLine, signal) => downStack(env.value, id.value, volumes.value, onLine, signal));
}

async function onRemove() {
  if (!window.confirm(`Delete the stack "${id.value}" and its files?`)) return;
  busy.value = true;
  actionError.value = '';
  try {
    await removeStack(env.value, id.value);
    await router.push({ name: 'stacks', params: { env: env.value } });
  } catch (e) {
    actionError.value = e instanceof Error ? e.message : String(e);
  } finally {
    busy.value = false;
  }
}

const shortCommit = (c: string) => c.slice(0, 12);
const onPull = () =>
  run('Pull and deploy', (onLine, signal) => pullStackGit(env.value, id.value, onLine, signal));

// gitForm is the source being edited, null when the form is closed.
const gitForm = ref<StackGitInput | null>(null);
const gitError = ref('');
function editGit() {
  gitError.value = '';
  const g = stack.value?.git;
  gitForm.value = g
    ? {
        repo_url: g.repo_url,
        ref: g.ref,
        compose_path: g.compose_path,
        env_path: g.env_path,
        auth: g.auth,
        username: g.username,
        secret: '',
        known_hosts: g.known_hosts,
        poll_seconds: g.poll_seconds,
      }
    : emptyStackGit();
}
async function onSaveGit() {
  if (!gitForm.value) return;
  if (dirty.value && !window.confirm('This replaces the compose file and drops your unsaved changes. Go on?'))
    return;
  gitError.value = '';
  busy.value = true;
  try {
    await setStackGit(env.value, id.value, gitForm.value);
    gitForm.value = null;
    saved.value = null; // The files changed on the server: follow them.
    compose.value = '';
    envFile.value = '';
    await reload();
  } catch (e) {
    gitError.value = e instanceof Error ? e.message : String(e);
  } finally {
    busy.value = false;
  }
}
async function onDetachGit() {
  if (
    !window.confirm(
      `Stop taking "${id.value}" from git? It keeps its current files, which you can then edit here.`,
    )
  )
    return;
  actionError.value = '';
  busy.value = true;
  try {
    await removeStackGit(env.value, id.value);
    await reload();
  } catch (e) {
    actionError.value = e instanceof Error ? e.message : String(e);
  } finally {
    busy.value = false;
  }
}

const editor = ref<InstanceType<typeof StackEditor> | null>(null);
// loadEnvExample reads the repository's env file for the .env form editor, if it has one.
const loadEnvExample = computed(() =>
  stack.value?.git?.env_path ? () => getStackEnvExample(env.value, id.value) : undefined,
);
const saveError = ref('');
const saveResult = ref('');

async function save(): Promise<boolean> {
  saveError.value = '';
  saveResult.value = '';
  busy.value = true;
  try {
    if (!(await editor.value?.validate())) return false;
    const files = { compose: compose.value, env: envFile.value };
    await updateStack(env.value, id.value, files);
    saved.value = files;
    // A reload that started before the save would bring back the old files; a new one
    // supersedes it.
    void reload();
    saveResult.value = 'Saved. The running stack changes at its next deploy.';
    return true;
  } catch (e) {
    saveError.value = e instanceof Error ? e.message : String(e);
    return false;
  } finally {
    busy.value = false;
  }
}

async function onSaveAndDeploy() {
  if (await save()) {
    saveResult.value = 'Saved.';
    await onDeploy();
  }
}

function onRevert() {
  if (!saved.value) return;
  compose.value = saved.value.compose;
  envFile.value = saved.value.env;
}
</script>

<template>
  <p><RouterLink :to="{ name: 'stacks', params: { env } }">← Stacks</RouterLink></p>
  <LoadState :loading="!stack" :error="stack ? '' : error" :ready="stack !== null" @retry="reload">
    <template v-if="stack">
      <p v-if="error" class="error" role="alert">Couldn't refresh the stack: {{ error }}</p>
      <h1>{{ stack.name }}</h1>
      <p class="muted">{{ stack.running }} of {{ stack.containers }} containers running</p>
      <div class="actions">
        <button type="button" class="primary" :disabled="busy" @click="onDeploy">Deploy</button>
        <button type="button" :disabled="busy" title="Pull newer images, then deploy" @click="onUpdate">
          Update
        </button>
        <button type="button" class="danger" :disabled="busy || stack.containers === 0" @click="onDown">
          Down
        </button>
        <label><input v-model="volumes" type="checkbox" :disabled="busy" /> remove volumes too</label>
        <button v-if="stack.containers === 0" type="button" class="danger" :disabled="busy" @click="onRemove">
          Delete stack
        </button>
      </div>
      <p v-if="actionError" class="error" role="alert">{{ actionError }}</p>
      <section v-if="outcome || output.length" class="card">
        <h2>Compose output</h2>
        <p class="muted" role="status">{{ outcome }}</p>
        <div ref="outputBox" class="output" tabindex="0" aria-label="Compose output">
          <div v-for="(l, i) in output" :key="i">{{ l.line }}</div>
        </div>
      </section>

      <FoldCard fold-id="stack.services">
        <template #title>Services</template>
        <p v-if="rows.length === 0" class="muted">No services.</p>
        <div v-else class="table-wrap">
          <table>
            <thead>
              <tr>
                <th>Service</th>
                <th>Container</th>
                <th>State</th>
                <th class="wide-only">Status</th>
                <th class="wide-only">Image</th>
              </tr>
            </thead>
            <tbody>
              <template v-for="svc in rows" :key="svc.name">
                <tr v-if="svc.containers.length === 0">
                  <td>{{ svc.name }}</td>
                  <td colspan="4" class="muted">not created</td>
                </tr>
                <tr v-for="(c, i) in svc.containers" :key="c.id">
                  <td>{{ i === 0 ? svc.name : '' }}</td>
                  <td class="break">
                    <RouterLink :to="{ name: 'container', params: { env, id: c.id } }">{{
                      c.name
                    }}</RouterLink>
                  </td>
                  <td><StateBadge :state="c.state" :health="c.health" /></td>
                  <td class="wide-only">{{ c.status }}</td>
                  <td class="wide-only break">
                    <code>{{ c.image }}</code>
                  </td>
                </tr>
              </template>
            </tbody>
          </table>
        </div>
      </FoldCard>

      <UpdateHistory ref="history" :env="env" kind="stack" :target="stack.name" />

      <WebhooksCard :key="`${env}/${stack.name}`" :env="env" kind="stack" :target="stack.name" />

      <FoldCard fold-id="stack.git" class="git-card">
        <template #title>Git</template>
        <template v-if="stack.git">
          <dl class="git-facts">
            <dt>Repository</dt>
            <dd class="break">
              <code>{{ stack.git.repo_url }}</code>
            </dd>
            <dt>Branch or tag</dt>
            <dd>{{ stack.git.ref || 'default branch' }}</dd>
            <dt>Compose file</dt>
            <dd class="break">
              <code>{{ stack.git.compose_path }}</code>
            </dd>
            <template v-if="stack.git.env_path">
              <dt>Env file</dt>
              <dd class="break">
                <code>{{ stack.git.env_path }}</code>
              </dd>
            </template>
            <dt>Commit</dt>
            <dd>
              <code :title="stack.git.commit">{{ shortCommit(stack.git.commit) }}</code>
            </dd>
            <dt>Checked</dt>
            <dd>
              {{ stack.git.poll_seconds ? `every ${stack.git.poll_seconds / 60} min` : 'on pull only' }}
            </dd>
          </dl>
          <p v-if="stack.git.last_error" class="error" role="alert">Last pull: {{ stack.git.last_error }}</p>
        </template>
        <p v-else class="muted">
          This stack's files are edited here. Take its compose file from a repository instead:
        </p>
        <div v-if="!gitForm" class="actions">
          <button v-if="stack.git" type="button" class="primary" :disabled="busy" @click="onPull">
            Pull and deploy
          </button>
          <button type="button" :disabled="busy" @click="editGit">
            {{ stack.git ? 'Change source' : 'Connect a repository' }}
          </button>
          <button v-if="stack.git" type="button" :disabled="busy" @click="onDetachGit">Detach</button>
        </div>
        <form v-else class="files" @submit.prevent="onSaveGit">
          <StackGitForm
            v-model="gitForm"
            :disabled="busy"
            :has-secret="stack.git && stack.git.auth !== 'none'"
          />
          <p v-if="gitError" class="error" role="alert">{{ gitError }}</p>
          <div class="actions">
            <button type="submit" class="primary" :disabled="busy">Save and read the files</button>
            <button type="button" :disabled="busy" @click="gitForm = null">Cancel</button>
            <span class="muted">The running stack changes at its next deploy.</span>
          </div>
        </form>
      </FoldCard>

      <FoldCard fold-id="stack.files">
        <template #title>Files</template>
        <form class="files" @submit.prevent="save">
          <StackEditor
            ref="editor"
            v-model:compose="compose"
            v-model:env="envFile"
            :disabled="busy"
            :compose-from-git="!!stack.git"
            :load-env-example="loadEnvExample"
          />
          <p v-if="saveError" class="error" role="alert">{{ saveError }}</p>
          <div class="actions">
            <button type="submit" class="primary" :disabled="busy || !dirty">Save</button>
            <button type="button" :disabled="busy || !dirty" @click="onSaveAndDeploy">Save and deploy</button>
            <button type="button" :disabled="busy || !dirty" @click="onRevert">Revert</button>
            <span v-if="dirty" class="muted">Unsaved changes.</span>
            <span v-else-if="saveResult" class="muted" role="status">{{ saveResult }}</span>
          </div>
        </form>
      </FoldCard>

      <StackLogs :env="env" :containers="logSources" />
    </template>
  </LoadState>
</template>

<style scoped>
h1 {
  margin-bottom: 0.25rem;
  word-break: break-all;
}

.actions {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 0.5rem;
  margin-bottom: 1rem;
}

.actions label {
  display: inline-flex;
  align-items: center;
  gap: 0.3rem;
}

.files .actions {
  margin: 0.75rem 0 0;
}

.git-facts {
  display: grid;
  grid-template-columns: max-content 1fr;
  gap: 0.25rem 1rem;
  margin: 0 0 0.75rem;
}

.git-facts dt {
  color: var(--muted);
}

.git-facts dd {
  margin: 0;
}

.git-card .actions {
  margin-bottom: 0;
}

.card + .card,
.card + section {
  margin-top: 1rem;
}

.output {
  max-height: 20rem;
  overflow: auto;
  padding: 0.5rem;
  border: 1px solid var(--border);
  border-radius: var(--radius);
  background: var(--bg);
  font-family: var(--mono);
  font-size: 0.85rem;
  white-space: pre-wrap;
  word-break: break-all;
}
</style>
