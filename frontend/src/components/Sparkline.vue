<script setup lang="ts">
import { computed } from 'vue';

// A small line chart in plain SVG: values left to right, scaled to max (or the largest
// value) so the line fills the height.
const props = defineProps<{ values: number[]; max?: number; label: string }>();

const points = computed(() => {
  const n = props.values.length;
  if (n === 0) return '';
  const top = Math.max(props.max ?? 0, ...props.values) || 1;
  return props.values
    .map((v, i) => {
      const x = n === 1 ? 100 : (i / (n - 1)) * 100;
      const y = 30 - (Math.max(v, 0) / top) * 28 - 1;
      return `${x.toFixed(2)},${y.toFixed(2)}`;
    })
    .join(' ');
});
</script>

<template>
  <svg class="sparkline" viewBox="0 0 100 30" preserveAspectRatio="none" role="img" :aria-label="label">
    <polyline v-if="points" :points="points" vector-effect="non-scaling-stroke" />
  </svg>
</template>

<style scoped>
.sparkline {
  display: block;
  width: 100%;
  height: 2.5rem;
  background: var(--accent-soft);
  border-radius: 4px;
}

polyline {
  fill: none;
  stroke: var(--accent);
  stroke-width: 1.5;
  stroke-linejoin: round;
}
</style>
