<script setup lang="ts">
import { onBeforeUnmount, ref, shallowRef, watch } from 'vue';
import { RouterLink } from 'vue-router';

import { getEnvironmentStats, type EnvironmentStats } from '../api/client';
import { formatBytes } from '../format';

const props = defineProps<{ env: string }>();

// refreshMs is the pause between one overview and the next request.
const refreshMs = 10_000;

const stats = shallowRef<EnvironmentStats | null>(null);
const error = ref('');
let timer: ReturnType<typeof setTimeout> | undefined;
// generation drops the answer of a request for an environment no longer shown.
let generation = 0;

async function load() {
  clearTimeout(timer);
  const gen = ++generation;
  try {
    const s = await getEnvironmentStats(props.env);
    if (gen !== generation) return;
    stats.value = s;
    error.value = '';
  } catch (e) {
    if (gen !== generation) return;
    error.value = e instanceof Error ? e.message : String(e);
  }
  timer = setTimeout(load, refreshMs);
}

watch(
  () => props.env,
  () => {
    stats.value = null;
    error.value = '';
    void load();
  },
  { immediate: true },
);
onBeforeUnmount(() => {
  generation++;
  clearTimeout(timer);
});
</script>

<template>
  <section class="card">
    <h2>Usage</h2>
    <p v-if="error" class="error" role="alert">{{ error }}</p>
    <p v-if="!stats && !error" class="muted">Sampling running containers…</p>
    <template v-if="stats">
      <p class="totals">
        <span
          ><strong>{{ stats.total.cpu_percent.toFixed(1) }}%</strong> CPU</span
        >
        <span
          ><strong>{{ formatBytes(stats.total.memory_usage) }}</strong> memory</span
        >
        <span>
          <strong>{{ formatBytes(stats.total.network_rx) }}</strong> in ·
          <strong>{{ formatBytes(stats.total.network_tx) }}</strong> out
        </span>
      </p>
      <p v-if="stats.containers.length === 0" class="muted">No running containers.</p>
      <div v-else class="table-wrap">
        <table>
          <thead>
            <tr>
              <th>Container</th>
              <th>CPU</th>
              <th>Memory</th>
              <th class="wide-only">Network in / out</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="c in stats.containers" :key="c.id">
              <td>
                <RouterLink :to="{ name: 'container', params: { env, id: c.id } }">{{ c.name }}</RouterLink>
              </td>
              <td>
                <meter min="0" max="100" :value="Math.min(c.cpu_percent, 100)" />
                {{ c.cpu_percent.toFixed(1) }}%
              </td>
              <td>
                <meter min="0" max="100" :value="c.memory_percent" />
                {{ formatBytes(c.memory_usage) }}
              </td>
              <td class="wide-only">{{ formatBytes(c.network_rx) }} / {{ formatBytes(c.network_tx) }}</td>
            </tr>
          </tbody>
        </table>
      </div>
    </template>
  </section>
</template>

<style scoped>
.totals {
  display: flex;
  flex-wrap: wrap;
  gap: 1.5rem;
}

meter {
  width: 4rem;
  vertical-align: middle;
}
</style>
