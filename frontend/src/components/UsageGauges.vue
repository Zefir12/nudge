<script setup lang="ts">
import { usageIcons as icons } from './usageIcons';

// UsageGauges shows load as rings in a column (a row when the tile is narrow); a gauge
// without a percent shows an empty ring and a dash.
export interface Gauge {
  label: string;
  percent: number | null;
  detail: string;
  tone: 'cpu' | 'mem' | 'disk';
  // TODO: remove with tileMock.ts; tags a gauge that shows mock data.
  mock?: boolean;
}

defineProps<{ gauges: Gauge[] }>();
</script>

<template>
  <div class="gauges">
    <div v-for="g in gauges" :key="g.label" class="gauge">
      <svg
        viewBox="0 0 36 36"
        role="img"
        :aria-label="`${g.label} ${g.percent === null ? 'unknown' : `${Math.round(g.percent)}%`}`"
      >
        <circle class="track" cx="18" cy="18" r="15.9155" />
        <circle
          v-if="g.percent !== null"
          class="ring"
          :class="g.tone"
          cx="18"
          cy="18"
          r="15.9155"
          :stroke-dasharray="`${g.percent} 100`"
          transform="rotate(-90 18 18)"
        />
        <text x="18" y="20.6" text-anchor="middle">
          {{ g.percent === null ? '–' : `${Math.round(g.percent)}%` }}
        </text>
      </svg>
      <span class="label">
        <svg class="icon" :class="g.tone" viewBox="0 0 24 24" aria-hidden="true">
          <path :d="icons[g.tone]" />
        </svg>
        {{ g.label }}
        <span v-if="g.mock" class="mock-tag" title="TODO: mock data until there is a disk usage endpoint"
          >mock</span
        >
      </span>
      <span class="detail">{{ g.detail }}</span>
    </div>
  </div>
</template>

<style scoped>
.gauges {
  display: grid;
  gap: 1rem;
  text-align: center;
}

@container (max-width: 520px) {
  .gauges {
    grid-template-columns: repeat(3, 1fr);
  }
}

.gauge {
  display: flex;
  flex-direction: column;
  align-items: center;
}

svg {
  display: block;
  width: 104px;
  margin-bottom: 0.2rem;
}

@container (max-width: 520px) {
  svg {
    width: 76px;
  }
}

.label {
  display: inline-flex;
  align-items: center;
  gap: 0.35rem;
  font-size: 12.5px;
  font-weight: 600;
}

.label .icon {
  width: 14px;
  height: 14px;
  margin: 0;
  fill: none;
  stroke-width: 1.8;
  stroke-linecap: round;
  stroke-linejoin: round;
}

.mock-tag {
  padding: 0 0.3rem;
  font-size: 9px;
  font-weight: 600;
  color: var(--warn);
  text-transform: uppercase;
  letter-spacing: 0.08em;
  background: var(--warn-soft);
  border-radius: var(--radius-sm);
}

.detail {
  font-family: var(--mono);
  font-size: 11px;
  color: var(--muted);
}

.track {
  fill: none;
  stroke: rgb(255 255 255 / 7%);
  stroke-width: 3.2;
}

.ring {
  fill: none;
  stroke-width: 3.2;
  stroke-linecap: round;
}

.cpu {
  stroke: var(--accent);
}

.mem {
  stroke: var(--series-2);
}

.disk {
  stroke: var(--series-3);
}

text {
  font-family: var(--font-display);
  font-size: 7.5px;
  font-weight: 600;
  fill: var(--text);
}
</style>
