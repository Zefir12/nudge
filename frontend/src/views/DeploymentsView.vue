<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue';
import { RouterLink, useRouter } from 'vue-router';

import {
  createDeployment,
  listDeployments,
  listStacks,
  type Deployment,
  type StackSummary,
} from '../api/client';
import MyAllowances from '../components/MyAllowances.vue';
import { environments, loadEnvironments } from '../environments';
import { currentUser } from '../session';

const router = useRouter();
const deployments = ref<Deployment[] | null>(null);
const error = ref('');
const busy = ref(false);

const name = ref('');
const environment = ref<number | null>(null);
const stack = ref('');
const description = ref('');
const variables = ref('');
const stacks = ref<StackSummary[]>([]);

const message = (e: unknown) => (e instanceof Error ? e.message : String(e));
// Creating a deployment, and the environments it needs, are for admins.
const isAdmin = computed(() => !!currentUser.value?.admin);

async function load() {
  try {
    deployments.value = await listDeployments();
  } catch (e) {
    error.value = message(e);
  }
}

// The stack list follows the chosen environment.
watch(environment, async (env) => {
  stack.value = '';
  stacks.value = [];
  if (env === null) return;
  try {
    stacks.value = await listStacks(String(env));
  } catch (e) {
    error.value = message(e);
  }
});

const envName = (id: number) => environments.value?.find((e) => e.id === id)?.name ?? `#${id}`;

// splitVariables reads "API_IMAGE, WORKER_IMAGE" or one per line.
const splitVariables = (text: string) => text.split(/[\s,]+/).filter((v) => v !== '');

const canCreate = computed(
  () => name.value.trim() !== '' && environment.value !== null && stack.value !== '',
);

async function onCreate() {
  if (environment.value === null) return;
  busy.value = true;
  error.value = '';
  try {
    const d = await createDeployment({
      name: name.value.trim(),
      environment: environment.value,
      stack: stack.value,
      description: description.value.trim(),
      variables: splitVariables(variables.value),
    });
    await router.push({ name: 'deployment', params: { name: d.name } });
  } catch (e) {
    error.value = message(e);
  } finally {
    busy.value = false;
  }
}

onMounted(() => {
  void load();
  if (isAdmin.value && !environments.value)
    void loadEnvironments().catch((e: unknown) => (error.value ||= message(e)));
});
</script>

<template>
  <h1>Deployments</h1>
  <p class="muted">
    A deployment is a stack that CI deploys through: it has its own deploy tokens and members, and every
    deploy is recorded as a run. A deploy can set the stack's <code>.env</code> variables listed here, such as
    the image a service runs (<code>image: ${API_IMAGE}</code> in the compose file).
  </p>

  <section v-if="isAdmin" class="card">
    <h2>New deployment</h2>
    <form class="create" @submit.prevent="onCreate">
      <label>
        Name
        <input v-model="name" name="name" maxlength="63" placeholder="e.g. shop" required />
      </label>
      <label>
        Environment
        <select v-model="environment" name="environment" required>
          <option :value="null" disabled>Choose…</option>
          <option v-for="e in environments ?? []" :key="e.id" :value="e.id">{{ e.name }}</option>
        </select>
      </label>
      <label>
        Stack
        <select v-model="stack" name="stack" :disabled="environment === null" required>
          <option value="" disabled>{{ stacks.length ? 'Choose…' : 'No stacks' }}</option>
          <option v-for="s in stacks" :key="s.name" :value="s.name">{{ s.name }}</option>
        </select>
      </label>
      <label class="wide">
        Variables deploys may set
        <input v-model="variables" name="variables" placeholder="e.g. API_IMAGE, WORKER_IMAGE" />
      </label>
      <label class="wide">
        Description
        <input v-model="description" name="description" maxlength="500" />
      </label>
      <button type="submit" class="primary" :disabled="busy || !canCreate">Create deployment</button>
    </form>
    <p v-if="error" class="error" role="alert">{{ error }}</p>
  </section>

  <section class="card">
    <p v-if="error && !isAdmin" class="error" role="alert">{{ error }}</p>
    <p v-if="deployments === null && !error" class="muted">Loading…</p>
    <p v-else-if="deployments && deployments.length === 0" class="muted">No deployments yet.</p>
    <div v-else-if="deployments" class="table-wrap">
      <table>
        <thead>
          <tr>
            <th>Name</th>
            <th>Environment</th>
            <th>Stack</th>
            <th>Your role</th>
            <th class="wide-only">Description</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="d in deployments" :key="d.name">
            <td>
              <RouterLink :to="{ name: 'deployment', params: { name: d.name } }">{{ d.name }}</RouterLink>
            </td>
            <td>{{ envName(d.environment) }}</td>
            <td>
              <RouterLink :to="{ name: 'stack', params: { env: String(d.environment), id: d.stack } }">{{
                d.stack
              }}</RouterLink>
            </td>
            <td>{{ d.role || 'admin' }}</td>
            <td class="wide-only">{{ d.description }}</td>
          </tr>
        </tbody>
      </table>
    </div>
  </section>

  <MyAllowances v-if="!isAdmin" />
</template>

<style scoped>
.card + .card {
  margin-top: 1rem;
}

.create {
  display: flex;
  align-items: flex-end;
  gap: 0.75rem;
  flex-wrap: wrap;
}

.create label {
  display: flex;
  flex-direction: column;
  gap: 0.25rem;
  font-weight: 500;
}

.create .wide {
  flex: 1 1 16rem;
}
</style>
