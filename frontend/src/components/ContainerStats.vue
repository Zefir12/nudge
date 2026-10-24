<script setup lang="ts">
import { computed, onBeforeUnmount, ref, shallowRef, watch } from 'vue';

import { streamStats, type StatsSample } from '../api/client';
import { formatBytes } from '../format';
import Sparkline from './Sparkline.vue';

const props = defineProps<{ env: string; id: string }>();

// maxSamples is about a minute of history.
const maxSamples = 60;

const samples = shallowRef<StatsSample[]>([]);
const ended = ref(false);
const error = ref('');
let close: (() => void) | null = null;

function open() {
  close?.();
  samples.value = [];
  ended.value = false;
  error.value = '';
  close = streamStats(props.env, props.id, {
    onSample: (s) => {
      samples.value = [...samples.value.slice(-(maxSamples - 1)), s];
    },
    onEnd: (e) => {
      ended.value = true;
      error.value = e ?? '';
    },
  });
}

watch(() => [props.env, props.id], open, { immediate: true });
onBeforeUnmount(() => close?.());

const last = computed(() => samples.value.at(-1));

// rates turns two totals into bytes per second between consecutive samples.
function rates(pick: (s: StatsSample) => number): number[] {
  const s = samples.value;
  const out: number[] = [];
  for (let i = 1; i < s.length; i++) {
    const secs = (Date.parse(s[i]!.time) - Date.parse(s[i - 1]!.time)) / 1000;
    out.push(secs > 0 ? Math.max(pick(s[i]!) - pick(s[i - 1]!), 0) / secs : 0);
  }
  return out;
}

const cpu = computed(() => samples.value.map((s) => s.cpu_percent));
const memory = computed(() => samples.value.map((s) => s.memory_usage));
const rx = computed(() => rates((s) => s.network_rx));
const tx = computed(() => rates((s) => s.network_tx));
const rate = (v: number[]) => (v.length ? `${formatBytes(v.at(-1)!)}/s` : '—');
</script>

<template>
  <section class="card">
    <h2>Usage</h2>
    <p v-if="error" class="error" role="alert">{{ error }}</p>
    <p v-else-if="ended" class="muted">The container stopped.</p>
    <p v-if="!last && !ended" class="muted">Waiting for the first sample…</p>
    <div v-if="last" class="charts">
      <div class="chart">
        <span class="label">CPU</span>
        <span class="value">{{ last.cpu_percent.toFixed(1) }}%</span>
        <Sparkline :values="cpu" :max="100" label="CPU over the last minute" />
      </div>
      <div class="chart">
        <span class="label">Memory</span>
        <span class="value">
          {{ formatBytes(last.memory_usage) }}
          <span v-if="last.memory_limit" class="muted">
            of {{ formatBytes(last.memory_limit) }} ({{ last.memory_percent.toFixed(1) }}%)
          </span>
        </span>
        <Sparkline :values="memory" :max="last.memory_limit" label="Memory over the last minute" />
      </div>
      <div class="chart">
        <span class="label">Network in</span>
        <span class="value"
          >{{ rate(rx) }} <span class="muted">· {{ formatBytes(last.network_rx) }} total</span></span
        >
        <Sparkline :values="rx" label="Network in over the last minute" />
      </div>
      <div class="chart">
        <span class="label">Network out</span>
        <span class="value"
          >{{ rate(tx) }} <span class="muted">· {{ formatBytes(last.network_tx) }} total</span></span
        >
        <Sparkline :values="tx" label="Network out over the last minute" />
      </div>
    </div>
    <p v-if="last" class="muted">
      Block I/O {{ formatBytes(last.block_read) }} read · {{ formatBytes(last.block_write) }} written ·
      {{ last.pids }} processes
    </p>
  </section>
</template>

<style scoped>
.charts {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(14rem, 1fr));
  gap: 1rem;
  margin-bottom: 0.75rem;
}

.chart {
  display: flex;
  flex-direction: column;
  gap: 0.25rem;
}

.label {
  color: var(--muted);
  font-size: 0.85rem;
}

.value {
  font-weight: 600;
}

.value .muted {
  font-weight: 400;
}
</style>
