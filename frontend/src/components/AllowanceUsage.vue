<script setup lang="ts">
import { computed } from 'vue';

import type { Allowance } from '../api/client';
import { formatBinaryBytes } from '../format';

const props = defineProps<{ allowance: Allowance }>();

const cpus = (milli: number) => `${milli / 1000}`;

// rows are what the user's containers take against each limit.
const rows = computed(() => {
  const { limits, used } = props.allowance;
  if (!limits) return [];
  return [
    {
      name: 'CPU',
      used: used.cpus_milli,
      max: limits.cpus_milli,
      label: `${cpus(used.cpus_milli)} / ${cpus(limits.cpus_milli)}`,
    },
    {
      name: 'Memory',
      used: used.memory_bytes,
      max: limits.memory_bytes,
      label: `${formatBinaryBytes(used.memory_bytes)} / ${formatBinaryBytes(limits.memory_bytes)}`,
    },
    {
      name: 'Disk',
      used: used.disk_bytes,
      max: limits.disk_bytes,
      label: `${formatBinaryBytes(used.disk_bytes)} / ${formatBinaryBytes(limits.disk_bytes)}`,
    },
    {
      name: 'Containers',
      used: used.containers,
      max: limits.max_containers,
      label: `${used.containers} / ${limits.max_containers}`,
    },
  ];
});

const ports = computed(() => {
  const l = props.allowance.limits;
  if (!l || l.port_min === 0) return 'none';
  return l.port_min === l.port_max ? `${l.port_min}` : `${l.port_min}–${l.port_max}`;
});
</script>

<template>
  <p v-if="!allowance.limits" class="muted none">No allowance here: you can't create or start containers.</p>
  <div v-else class="usage">
    <label v-for="r in rows" :key="r.name">
      {{ r.name }}
      <meter
        min="0"
        :max="Math.max(r.max, 1)"
        :high="r.max * 0.9"
        :value="Math.min(r.used, Math.max(r.max, 1))"
      />
      <span class="val">{{ r.label }}</span>
    </label>
    <p class="muted extra">
      Host ports: <span class="val">{{ ports }}</span>
      <template v-if="allowance.limits.images.length">
        · Images: <span class="val">{{ allowance.limits.images.join(', ') }}</span>
      </template>
    </p>
  </div>
</template>

<style scoped>
.none {
  margin: 0;
}

.usage {
  display: flex;
  flex-direction: column;
  gap: 0.4rem;
}

.usage label {
  display: grid;
  grid-template-columns: 6rem minmax(0, 1fr) auto;
  align-items: center;
  gap: 0.75rem;
  font-size: 12px;
  color: var(--muted);
}

.usage meter {
  width: 100%;
  height: 0.5rem;
}

.val {
  font-family: var(--mono);
  color: var(--text);
  white-space: nowrap;
}

.extra {
  margin: 0.25rem 0 0;
  font-size: 12px;
}
</style>
