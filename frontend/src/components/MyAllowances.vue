<script setup lang="ts">
import { onMounted, ref } from 'vue';

import { getAllowance, type Allowance, type Environment } from '../api/client';
import { loadEnvironments } from '../environments';
import AllowanceUsage from './AllowanceUsage.vue';

// items are the user's environments with their allowance there, or why it couldn't load
// (an offline environment can't count its containers).
const items = ref<{ env: Environment; allowance?: Allowance; error?: string }[] | null>(null);
const error = ref('');

const message = (e: unknown) => (e instanceof Error ? e.message : String(e));

onMounted(async () => {
  try {
    const envs = await loadEnvironments();
    items.value = await Promise.all(
      envs.map(async (env) => {
        try {
          return { env, allowance: await getAllowance(String(env.id)) };
        } catch (e) {
          return { env, error: message(e) };
        }
      }),
    );
  } catch (e) {
    error.value = message(e);
  }
});
</script>

<template>
  <section class="card">
    <h2>Your allowance</h2>
    <p class="muted">What your containers may take in total, running or not, in each environment.</p>
    <p v-if="error" class="error" role="alert">{{ error }}</p>
    <p v-else-if="items === null" class="muted">Loading…</p>
    <p v-else-if="items.length === 0" class="muted">No environment has been given to you yet.</p>
    <div v-for="item in items ?? []" :key="item.env.id" class="env">
      <h3>{{ item.env.name }}</h3>
      <p v-if="item.error" class="error">{{ item.error }}</p>
      <AllowanceUsage v-else-if="item.allowance" :allowance="item.allowance" />
    </div>
  </section>
</template>

<style scoped>
.env + .env {
  margin-top: 1rem;
}

h3 {
  margin: 0 0 0.5rem;
  font-size: 14px;
}
</style>
