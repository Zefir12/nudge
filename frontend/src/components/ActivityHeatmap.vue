<script setup lang="ts">
import { computed } from 'vue';

// ActivityHeatmap shows event counts per hour for the last 7 days: counts holds 7×24
// numbers, oldest day first, and days their labels. A cell's shade is its count against
// the busiest hour; hovering one names its count.
const props = defineProps<{ counts: number[]; days: string[] }>();

const max = computed(() => Math.max(1, ...props.counts));

function tip(i: number): string {
  const n = props.counts[i];
  const h = String(i % 24).padStart(2, '0');
  return `${props.days[Math.floor(i / 24)]} ${h}:00, ${n} ${n === 1 ? 'event' : 'events'}`;
}
</script>

<template>
  <div class="heatmap">
    <div class="title"><span>Container events per hour</span><span>last 7 days</span></div>
    <div class="body">
      <ul class="days">
        <li v-for="d in days" :key="d">{{ d }}</li>
      </ul>
      <!-- The viewBox's own aspect keeps every cell square at any width. -->
      <svg viewBox="0 0 24 7">
        <rect
          v-for="(n, i) in counts"
          :key="i"
          :x="(i % 24) + 0.08"
          :y="Math.floor(i / 24) + 0.08"
          width="0.84"
          height="0.84"
          rx="0.15"
          :fill-opacity="0.08 + (n / max) * 0.92"
        >
          <title>{{ tip(i) }}</title>
        </rect>
      </svg>
      <div class="axis"><span>00</span><span>06</span><span>12</span><span>18</span><span>24</span></div>
    </div>
  </div>
</template>

<style scoped>
.title {
  display: flex;
  justify-content: space-between;
  margin-bottom: 0.35rem;
  font-size: 12px;
  color: var(--muted);
}

.body {
  display: grid;
  grid-template-columns: auto minmax(0, 21rem);
  gap: 0.2rem 0.5rem;
  justify-content: center;
}

.days {
  display: grid;
  grid-template-rows: repeat(7, 1fr);
  margin: 0;
  padding: 0;
  font-family: var(--mono);
  font-size: 10px;
  line-height: 1;
  color: var(--faint);
  list-style: none;
}

.days li {
  display: flex;
  align-items: center;
}

svg {
  display: block;
  width: 100%;
  height: auto;
}

rect {
  fill: var(--accent);
}

rect:hover {
  stroke: var(--text);
  stroke-width: 0.08;
}

.axis {
  display: flex;
  grid-column: 2;
  justify-content: space-between;
  font-family: var(--mono);
  font-size: 10px;
  color: var(--faint);
}
</style>
