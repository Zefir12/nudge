<script setup lang="ts">
import { computed, onUnmounted, ref, watch } from 'vue';
import { RouterLink, useRoute, useRouter } from 'vue-router';

import {
  changeDeployment,
  createDeployToken,
  deleteDeployToken,
  deleteDeployment,
  deploy,
  getDeployment,
  listDeploymentMembers,
  listDeploymentRuns,
  listDeployTokens,
  removeDeploymentMember,
  setDeploymentMember,
  type CreatedDeployToken,
  type Deployment,
  type DeploymentAction,
  type DeploymentMember,
  type DeploymentRole,
  type DeploymentRun,
  type DeployScope,
  type DeployToken,
  type DeploymentHealth,
} from '../api/client';
import DeploymentHealthForm from '../components/DeploymentHealthForm.vue';
import DeploySnippets from '../components/DeploySnippets.vue';
import { formatDate } from '../format';

const route = useRoute();
const router = useRouter();
const name = computed(() => String(route.params.name));

const deployment = ref<Deployment | null>(null);
const runs = ref<DeploymentRun[]>([]);
const tokens = ref<DeployToken[]>([]);
const members = ref<DeploymentMember[]>([]);
const error = ref('');
const busy = ref(false);

const message = (e: unknown) => (e instanceof Error ? e.message : String(e));
const can = (a: DeploymentAction) => deployment.value?.can.includes(a) ?? false;

async function load() {
  error.value = '';
  try {
    const d = await getDeployment(name.value);
    deployment.value = d;
    const [r, m, t] = await Promise.all([
      listDeploymentRuns(d.name),
      listDeploymentMembers(d.name),
      d.can.includes('manage_tokens') ? listDeployTokens(d.name) : Promise.resolve([]),
    ]);
    runs.value = r;
    members.value = m;
    tokens.value = t;
    editDescription.value = d.description;
    editVariables.value = d.variables.join(', ');
  } catch (e) {
    error.value = message(e);
  }
}

// run does one action, then reloads; the buttons stay disabled until both end.
async function run(action: () => Promise<void>) {
  busy.value = true;
  error.value = '';
  try {
    await action();
    await load();
  } catch (e) {
    error.value = message(e);
  } finally {
    busy.value = false;
  }
}

// While a run is under way the runs refresh every few seconds.
let poll: ReturnType<typeof setInterval> | undefined;
const underWay = computed(() => runs.value.some((r) => r.status === 'queued' || r.status === 'running'));
watch(underWay, (on) => {
  clearInterval(poll);
  poll = on
    ? setInterval(() => {
        void listDeploymentRuns(name.value)
          .then((r) => (runs.value = r))
          .catch(() => {});
      }, 3000)
    : undefined;
});
onUnmounted(() => clearInterval(poll));
watch(name, load, { immediate: true });

// Deploy: one optional value per variable; empty ones are left as they are.
const values = ref<Record<string, string>>({});
function onDeploy() {
  const vars: Record<string, string> = {};
  for (const [k, v] of Object.entries(values.value)) if (v.trim()) vars[k] = v.trim();
  void run(async () => {
    await deploy(name.value, vars);
  });
}

// Settings.
const editDescription = ref('');
const editVariables = ref('');
const onSave = () =>
  run(async () => {
    await changeDeployment(name.value, {
      description: editDescription.value.trim(),
      variables: editVariables.value.split(/[\s,]+/).filter((v) => v !== ''),
    });
  });
const onSaveHealth = (health: DeploymentHealth) =>
  run(async () => {
    await changeDeployment(name.value, { health });
  });

function onDelete() {
  if (!window.confirm(`Delete the deployment "${name.value}"? Its tokens stop working. The stack stays.`))
    return;
  void run(async () => {
    await deleteDeployment(name.value);
    await router.push({ name: 'deployments' });
  });
}

// Tokens.
const tokenName = ref('');
const tokenScopes = ref<DeployScope[]>(['deploy', 'runs:read']);
const created = ref<CreatedDeployToken | null>(null);
const onCreateToken = () =>
  run(async () => {
    created.value = await createDeployToken(name.value, {
      name: tokenName.value.trim(),
      scopes: tokenScopes.value,
    });
    tokenName.value = '';
  });
function onRevokeToken(t: DeployToken) {
  if (!window.confirm(`Revoke the token "${t.name}"? CI jobs using it stop working.`)) return;
  void run(async () => {
    await deleteDeployToken(name.value, t.id);
    if (created.value?.id === t.id) created.value = null;
  });
}

// Members.
const roles: DeploymentRole[] = ['viewer', 'deployer', 'maintainer', 'owner'];
const memberName = ref('');
const memberRole = ref<DeploymentRole>('deployer');
const onAddMember = () =>
  run(async () => {
    await setDeploymentMember(name.value, memberName.value.trim(), memberRole.value);
    memberName.value = '';
  });
const onRole = (m: DeploymentMember, role: DeploymentRole) =>
  run(async () => {
    await setDeploymentMember(name.value, m.username, role);
  });
function onRemoveMember(m: DeploymentMember) {
  if (!window.confirm(`Remove ${m.username} from "${name.value}"?`)) return;
  void run(async () => {
    await removeDeploymentMember(name.value, m.user_id);
  });
}

const varsText = (vars: Record<string, string>) =>
  Object.entries(vars)
    .map(([k, v]) => `${k}=${v}`)
    .join(' ');
</script>

<template>
  <h1>{{ name }}</h1>
  <p v-if="error" class="error" role="alert">{{ error }}</p>
  <p v-if="!deployment && !error" class="muted">Loading…</p>

  <template v-if="deployment">
    <p class="muted">
      Stack
      <RouterLink
        :to="{ name: 'stack', params: { env: String(deployment.environment), id: deployment.stack } }"
        >{{ deployment.stack }}</RouterLink
      >
      <template v-if="deployment.description"> · {{ deployment.description }}</template>
      · your role: {{ deployment.role || 'admin' }}
    </p>

    <section v-if="can('deploy')" class="card">
      <h2>Deploy</h2>
      <form class="row" @submit.prevent="onDeploy">
        <label v-for="v in deployment.variables" :key="v">
          {{ v }}
          <input v-model="values[v]" :name="v" placeholder="unchanged" />
        </label>
        <button type="submit" class="primary" :disabled="busy">Deploy</button>
      </form>
      <p class="muted">
        Pulls the stack's images and brings it up; set values replace the stack's .env lines.
      </p>
    </section>

    <section class="card">
      <h2>Runs</h2>
      <p v-if="runs.length === 0" class="muted">No runs yet.</p>
      <div v-else class="table-wrap">
        <table>
          <thead>
            <tr>
              <th>Run</th>
              <th>Status</th>
              <th>By</th>
              <th>Variables</th>
              <th class="wide-only">Images after</th>
              <th>Queued</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="r in runs" :key="r.id">
              <td>#{{ r.id }}</td>
              <td>
                <span class="status" :class="r.status">{{ r.status.replace('_', ' ') }}</span>
                <span v-if="r.status === 'running' && r.step" class="muted">
                  · {{ r.step.replace('_', ' ') }}</span
                >
                <div v-if="r.error" class="muted">{{ r.error }}</div>
                <div v-for="(w, i) in r.warnings ?? []" :key="i" class="warning">{{ w }}</div>
              </td>
              <td>{{ r.actor_kind === 'token' ? `token ${r.actor}` : r.actor }}</td>
              <td class="break">
                <code v-if="Object.keys(r.variables).length">{{ varsText(r.variables) }}</code>
              </td>
              <td class="wide-only break">
                <code v-if="r.image_after">{{ r.image_after }}</code>
              </td>
              <td>{{ formatDate(r.queued_at) }}</td>
            </tr>
          </tbody>
        </table>
      </div>
    </section>

    <section v-if="can('manage_tokens')" class="card">
      <h2>Deploy tokens</h2>
      <p class="muted">
        A deploy token works only on this deployment's deploys and runs, nowhere else in the API.
      </p>
      <div v-if="created" class="created" aria-live="polite">
        <p>Token "{{ created.name }}" created. Copy it now: it is shown only once.</p>
        <code class="secret">{{ created.token }}</code>
        <p>Store it as the CI secret <code>NUDGE_DEPLOY_TOKEN</code> for the step below.</p>
        <button type="button" @click="created = null">Done</button>
      </div>
      <form class="row" @submit.prevent="onCreateToken">
        <label>
          Name
          <input
            v-model="tokenName"
            name="token-name"
            maxlength="64"
            placeholder="e.g. forgejo ci"
            required
          />
        </label>
        <label class="check"><input v-model="tokenScopes" type="checkbox" value="deploy" /> deploy</label>
        <label class="check"
          ><input v-model="tokenScopes" type="checkbox" value="runs:read" /> read runs</label
        >
        <button type="submit" :disabled="busy || tokenScopes.length === 0">Create token</button>
      </form>
      <DeploySnippets :name="deployment.name" :variables="deployment.variables" />
      <div v-if="tokens.length" class="table-wrap">
        <table>
          <thead>
            <tr>
              <th>Name</th>
              <th>Token</th>
              <th>Scopes</th>
              <th>Last used</th>
              <th></th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="t in tokens" :key="t.id">
              <td>{{ t.name }}</td>
              <td>
                <code>{{ t.prefix }}…</code>
              </td>
              <td>{{ t.scopes.join(', ') }}</td>
              <td>{{ formatDate(t.last_used_at, 'never') }}</td>
              <td>
                <button type="button" class="danger" :disabled="busy" @click="onRevokeToken(t)">
                  Revoke
                </button>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </section>

    <section class="card">
      <h2>Members</h2>
      <div class="table-wrap">
        <table>
          <tbody>
            <tr v-for="m in members" :key="m.user_id">
              <td>{{ m.username }}</td>
              <td>
                <select
                  v-if="can('manage_members')"
                  :value="m.role"
                  :aria-label="`Role of ${m.username}`"
                  :disabled="busy"
                  @change="onRole(m, ($event.target as HTMLSelectElement).value as DeploymentRole)"
                >
                  <option v-for="r in roles" :key="r" :value="r">{{ r }}</option>
                </select>
                <template v-else>{{ m.role }}</template>
              </td>
              <td>
                <button
                  v-if="can('manage_members')"
                  type="button"
                  class="danger"
                  :disabled="busy"
                  @click="onRemoveMember(m)"
                >
                  Remove
                </button>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
      <form v-if="can('manage_members')" class="row" @submit.prevent="onAddMember">
        <label>
          Username
          <input v-model="memberName" name="member" required />
        </label>
        <label>
          Role
          <select v-model="memberRole" name="role">
            <option v-for="r in roles" :key="r" :value="r">{{ r }}</option>
          </select>
        </label>
        <button type="submit" :disabled="busy">Add member</button>
      </form>
    </section>

    <section v-if="can('edit') || can('delete')" class="card">
      <h2>Settings</h2>
      <form v-if="can('edit')" class="row" @submit.prevent="onSave">
        <label class="wide">
          Variables deploys may set
          <input v-model="editVariables" name="variables" />
        </label>
        <label class="wide">
          Description
          <input v-model="editDescription" name="description" maxlength="500" />
        </label>
        <button type="submit" :disabled="busy">Save</button>
      </form>
      <DeploymentHealthForm
        v-if="can('edit')"
        :health="deployment.health"
        :busy="busy"
        @save="onSaveHealth"
      />
      <button v-if="can('delete')" type="button" class="danger" :disabled="busy" @click="onDelete">
        Delete deployment
      </button>
    </section>
  </template>
</template>

<style scoped>
.card {
  margin-top: 1rem;
}

.row {
  display: flex;
  align-items: flex-end;
  gap: 0.75rem;
  flex-wrap: wrap;
  margin: 0.75rem 0;
}

.row label {
  display: flex;
  flex-direction: column;
  gap: 0.25rem;
  font-weight: 500;
}

.row .check {
  flex-direction: row;
  align-items: center;
  font-weight: normal;
}

.row .wide {
  flex: 1 1 16rem;
}

.created {
  margin: 0.75rem 0;
}

.secret {
  display: block;
  overflow-x: auto;
  margin: 0.5rem 0;
  white-space: pre;
}

.status.succeeded {
  color: var(--ok);
}

.status.failed,
.status.rolled_back {
  color: var(--danger);
}

.warning {
  color: var(--warn);
}
</style>
