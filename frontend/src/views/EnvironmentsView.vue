<script setup lang="ts">
import { onMounted, ref } from 'vue';
import { RouterLink } from 'vue-router';

import {
  createEnvironment,
  createJoinToken,
  revokeAgent,
  type Environment,
  type JoinToken,
} from '../api/client';
import EnvStatus from '../components/EnvStatus.vue';
import JoinTokenCard from '../components/JoinTokenCard.vue';
import { environments, loadEnvironments } from '../environments';
import { formatDate } from '../format';

const error = ref('');
const loading = ref(true);
const name = ref('');
const busy = ref(false);
// shown holds a new join token until the user dismisses it; it is never shown again.
const shown = ref<{ id: number; name: string; joinToken: JoinToken } | null>(null);

const message = (e: unknown) => (e instanceof Error ? e.message : String(e));

// load refreshes the shared list, so the switcher sees changes too. A failed action's
// error wins over a failed reload's.
async function load() {
  try {
    await loadEnvironments();
  } catch (e) {
    error.value ||= message(e);
  } finally {
    loading.value = false;
  }
}

// run does one action, then reloads the list; the buttons stay disabled until both end.
async function run(action: () => Promise<void>) {
  busy.value = true;
  error.value = '';
  try {
    await action();
  } catch (e) {
    error.value = message(e);
  }
  try {
    await load();
  } finally {
    busy.value = false;
  }
}

const onCreate = () =>
  run(async () => {
    const env = await createEnvironment(name.value.trim());
    shown.value = { id: env.id, name: env.name, joinToken: env.join_token };
    name.value = '';
  });

function onNewToken(env: Environment) {
  const note = env.agent?.enrolled
    ? 'The connected agent keeps working until an agent enrolls with the new token.'
    : 'Any earlier join token stops working.';
  if (!window.confirm(`Create a new join token for "${env.name}"? ${note}`)) return;
  void run(async () => {
    shown.value = { id: env.id, name: env.name, joinToken: await createJoinToken(env.id) };
  });
}

function onRevoke(env: Environment) {
  const msg = `Revoke the agent of "${env.name}"? It is disconnected and needs a new join token to connect again.`;
  if (!window.confirm(msg)) return;
  void run(async () => {
    await revokeAgent(env.id);
    if (shown.value?.id === env.id) shown.value = null;
  });
}

onMounted(load);
</script>

<template>
  <h1>Environments</h1>
  <p class="muted">
    Each environment is a Docker host. Remote hosts run <code>nudge agent</code>, which connects out to this
    server.
  </p>

  <JoinTokenCard v-if="shown" :name="shown.name" :join-token="shown.joinToken" @done="shown = null" />

  <section class="card">
    <form class="create" @submit.prevent="onCreate">
      <label>
        Name
        <input v-model="name" name="name" maxlength="64" placeholder="e.g. nas" required />
      </label>
      <button type="submit" class="primary" :disabled="busy">Add environment</button>
    </form>
    <p v-if="error" class="error" role="alert">{{ error }}</p>

    <p v-if="loading && !environments" class="muted">Loading…</p>
    <div v-else-if="environments" class="table-wrap">
      <table>
        <thead>
          <tr>
            <th>Name</th>
            <th>Kind</th>
            <th>Status</th>
            <th>Agent</th>
            <th>Created</th>
            <th></th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="env in environments" :key="env.id">
            <td>
              <RouterLink :to="{ name: 'env', params: { env: String(env.id) } }">{{ env.name }}</RouterLink>
            </td>
            <td>{{ env.kind }}</td>
            <td><EnvStatus :env="env" /></td>
            <td>
              <template v-if="env.kind === 'agent'">{{ env.agent?.version || '—' }}</template>
            </td>
            <td>{{ formatDate(env.created_at) }}</td>
            <td class="row-actions">
              <template v-if="env.kind === 'agent'">
                <button type="button" :disabled="busy" @click="onNewToken(env)">New join token</button>
                <button
                  v-if="env.agent?.enrolled"
                  type="button"
                  class="danger"
                  :disabled="busy"
                  @click="onRevoke(env)"
                >
                  Revoke
                </button>
              </template>
            </td>
          </tr>
        </tbody>
      </table>
    </div>
  </section>
</template>

<style scoped>
.card + .card,
.created + .card {
  margin-top: 1rem;
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

.row-actions {
  display: flex;
  gap: 0.5rem;
  justify-content: flex-end;
  white-space: nowrap;
}
</style>
