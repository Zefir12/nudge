<script setup lang="ts">
import { usageIcons as icons } from './usageIcons';

// UsageHistory charts CPU and memory side by side: percentages oldest first, scaled to
// 100, under the current value.
defineProps<{ cpu: number[]; mem: number[]; cpuNow: number | null; memNow: number | null }>();

// line and area paths in a 100×30 box.
function line(values: number[]): string {
  const n = values.length;
  return values
    .map(
      (v, i) =>
        `${i ? 'L' : 'M'}${((i / Math.max(n - 1, 1)) * 100).toFixed(2)},${(29 - (v / 100) * 27).toFixed(2)}`,
    )
    .join(' ');
}
const area = (values: number[]) => `${line(values)} L100,30 L0,30 Z`;
const pct = (v: number | null) => (v === null ? '–' : `${v.toFixed(v < 10 ? 1 : 0)}%`);
</script>

<template>
  <div class="charts">
    <div
      v-for="c in [
        { k: 'CPU', v: cpu, now: cpuNow, tone: 'cpu' as const },
        { k: 'Memory', v: mem, now: memNow, tone: 'mem' as const },
      ]"
      :key="c.k"
      class="chart"
    >
      <div class="head">
        <span class="name">
          <svg class="icon" :class="c.tone" viewBox="0 0 24 24" aria-hidden="true">
            <path :d="icons[c.tone]" />
          </svg>
          {{ c.k }} · 24h
        </span>
        <strong>{{ pct(c.now) }}</strong>
      </div>
      <svg
        viewBox="0 0 100 30"
        preserveAspectRatio="none"
        role="img"
        :aria-label="`${c.k} over the last 24 hours`"
      >
        <path class="area" :class="c.tone" :d="area(c.v)" />
        <path class="line" :class="c.tone" :d="line(c.v)" vector-effect="non-scaling-stroke" />
      </svg>
    </div>
  </div>
</template>

<style scoped>
.charts {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 0.9rem;
}

svg {
  display: block;
  width: 100%;
  height: 3.6rem;
}

.name {
  display: inline-flex;
  align-items: center;
  gap: 0.35rem;
}

.name .icon {
  width: 14px;
  height: 14px;
  fill: none;
  stroke-width: 1.8;
  stroke-linecap: round;
  stroke-linejoin: round;
}

.icon.cpu {
  stroke: var(--accent);
}

.icon.mem {
  stroke: var(--series-2);
}

.head {
  display: flex;
  align-items: baseline;
  justify-content: space-between;
  margin-bottom: 0.25rem;
  font-size: 12px;
  color: var(--muted);
}

.head strong {
  font-family: var(--font-display);
  font-size: 15px;
  color: var(--text);
}

.line {
  fill: none;
  stroke-width: 1.5;
  stroke-linejoin: round;
}

.line.cpu {
  stroke: var(--accent);
}

.line.mem {
  stroke: var(--series-2);
}

.area.cpu {
  fill: rgb(255 122 48 / 14%);
}

.area.mem {
  fill: rgb(154 164 184 / 10%);
}
</style>
