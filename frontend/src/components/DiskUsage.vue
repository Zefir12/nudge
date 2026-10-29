<script setup lang="ts">
import { computed } from 'vue';

import { formatBytes } from '../format';

// DiskUsage shows what takes the disk as one bar of the whole, with a legend and what a
// prune would free.
const props = defineProps<{
  total: number;
  parts: { label: string; bytes: number }[];
  reclaimable?: string;
}>();

const used = computed(() => props.parts.reduce((s, p) => s + p.bytes, 0));
// segments as percentages of the whole disk.
const segments = computed(() => {
  let x = 0;
  return props.parts.map((p, i) => {
    const w = (p.bytes / props.total) * 100;
    const seg = { ...p, x, w, tone: `s${i + 1}` };
    x += w;
    return seg;
  });
});
const usedPct = computed(() => (used.value / props.total) * 100);
</script>

<template>
  <div class="disk">
    <div class="big">
      {{ formatBytes(used) }}<span class="of"> of {{ formatBytes(total) }} used</span>
    </div>
    <svg
      viewBox="0 0 100 6"
      preserveAspectRatio="none"
      role="img"
      :aria-label="`Disk ${Math.round(usedPct)}% used`"
    >
      <rect
        v-for="s in segments"
        :key="s.label"
        :class="s.tone"
        :x="s.x"
        :width="Math.max(s.w - 0.5, 0)"
        y="0"
        height="6"
        rx="0.8"
      />
      <rect class="free" :x="usedPct" y="0" :width="Math.max(100 - usedPct, 0)" height="6" rx="0.8" />
    </svg>
    <ul class="legend">
      <li v-for="s in segments" :key="s.label">
        <i :class="s.tone" />{{ s.label }} <span class="muted">{{ formatBytes(s.bytes) }}</span>
      </li>
    </ul>
    <p v-if="reclaimable" class="reclaim">{{ reclaimable }}</p>
  </div>
</template>

<style scoped>
.big {
  margin-bottom: 0.45rem;
  font-family: var(--font-display);
  font-size: 22px;
  font-weight: 600;
}

.of {
  font-family: var(--font);
  font-size: 13px;
  font-weight: 400;
  color: var(--muted);
}

svg {
  display: block;
  width: 100%;
  height: 12px;
}

.free {
  fill: rgb(255 255 255 / 7%);
}

.s1 {
  fill: var(--accent);
  background: var(--accent);
}

.s2 {
  fill: #c2703a;
  background: #c2703a;
}

.s3 {
  fill: var(--series-2);
  background: var(--series-2);
}

.s4 {
  fill: #5d6577;
  background: #5d6577;
}

.legend {
  display: flex;
  flex-wrap: wrap;
  gap: 0.3rem 1rem;
  margin: 0.55rem 0 0;
  padding: 0;
  font-size: 12px;
  color: var(--text-soft);
  list-style: none;
}

li > i {
  display: inline-block;
  width: 8px;
  height: 8px;
  margin-right: 0.4rem;
  border-radius: 2px;
}

.reclaim {
  margin: 0.65rem 0 0;
  padding: 0.45rem 0.65rem;
  font-size: 12px;
  color: var(--accent-strong);
  background: var(--accent-soft);
  border-radius: var(--radius);
}
</style>
