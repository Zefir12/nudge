<script setup lang="ts">
import { computed, onBeforeUnmount, ref, shallowRef, toRef, watch } from 'vue';
import { RouterLink } from 'vue-router';

import { getEnvironmentStats, type EnvironmentStats } from '../api/client';
import { useLiveUsage } from '../composables/useLiveUsage';
import { formatBytes } from '../format';

const props = defineProps<{ env: string }>();

// busiest is how many containers the table shows until all are asked for.
const busiest = 5;

// The server's live usage stream updates the card with every sample. When it can't be
// opened before a first sample (collection is off), one overview is asked for instead.
const { usage: live, error: liveError } = useLiveUsage(toRef(props, 'env'));
const fallback = shallowRef<EnvironmentStats | null>(null);
const fallbackError = ref('');
// request is the overview in flight; stop aborts it, so the daemon stops sampling too.
let request: AbortController | null = null;
let triedFallback = false;
const showAll = ref(false);

const stats = computed<EnvironmentStats | null>(() => live.value ?? fallback.value);
const error = computed(() => (stats.value ? '' : fallbackError.value || liveError.value));

// The busiest containers by CPU, then memory, come first.
const sorted = computed(() =>
  [...(stats.value?.containers ?? [])].sort(
    (a, b) => b.cpu_percent - a.cpu_percent || b.memory_usage - a.memory_usage,
  ),
);
const shown = computed(() => (showAll.value ? sorted.value : sorted.value.slice(0, busiest)));

function stop() {
  request?.abort();
  request = null;
}

async function loadOverview() {
  triedFallback = true;
  const ctrl = new AbortController();
  request = ctrl;
  try {
    const s = await getEnvironmentStats(props.env, ctrl.signal);
    if (ctrl.signal.aborted) return;
    fallback.value = s;
  } catch (e) {
    if (ctrl.signal.aborted) return;
    fallbackError.value = e instanceof Error ? e.message : String(e);
  }
  request = null;
}

watch(liveError, (e) => {
  if (e && !live.value && !triedFallback) void loadOverview();
});

watch(
  () => props.env,
  () => {
    stop();
    fallback.value = null;
    fallbackError.value = '';
    triedFallback = false;
    showAll.value = false;
  },
);
onBeforeUnmount(stop);
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
      <p v-if="stats.skipped" class="muted">
        {{ stats.skipped }} running {{ stats.skipped === 1 ? 'container' : 'containers' }} could not be
        sampled.
      </p>
      <p v-if="stats.containers.length === 0 && !stats.skipped" class="muted">No running containers.</p>
      <template v-else>
        <h3>Busiest containers</h3>
        <div class="table-wrap">
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
              <tr v-for="c in shown" :key="c.id">
                <td>
                  <RouterLink :to="{ name: 'container', params: { env, id: c.id } }">{{ c.name }}</RouterLink>
                </td>
                <td>{{ c.cpu_percent.toFixed(1) }}%</td>
                <td>
                  <meter min="0" max="100" :value="c.memory_percent" />
                  {{ formatBytes(c.memory_usage) }}
                </td>
                <td class="wide-only">{{ formatBytes(c.network_rx) }} / {{ formatBytes(c.network_tx) }}</td>
              </tr>
            </tbody>
          </table>
        </div>
        <button v-if="sorted.length > busiest" type="button" class="all" @click="showAll = !showAll">
          {{ showAll ? `Show the busiest ${busiest}` : `Show all ${sorted.length}` }}
        </button>
      </template>
    </template>
  </section>
</template>

<style scoped>
.totals {
  display: flex;
  flex-wrap: wrap;
  gap: 1.5rem;
}

.all {
  margin-top: 0.75rem;
}

meter {
  width: 4rem;
  vertical-align: middle;
}
</style>
