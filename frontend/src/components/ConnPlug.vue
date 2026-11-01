<script setup lang="ts">
// ConnPlug shows whether an environment is connected: a plug in its socket glowing orange
// when online, pulled apart and grey when not (amber while it waits for its agent).
defineProps<{ online: boolean; waiting?: boolean }>();
</script>

<template>
  <span class="plug" :class="{ on: online, warn: !online && waiting }" aria-hidden="true">
    <svg viewBox="0 0 24 24">
      <g class="left">
        <path d="M1 12h4M5 6.5h5.5A1.5 1.5 0 0 1 12 8v8a1.5 1.5 0 0 1-1.5 1.5H5zM12 9.5h2.5M12 14.5h2.5" />
      </g>
      <g class="right">
        <path d="M13.5 6.5h4A1.5 1.5 0 0 1 19 8v8a1.5 1.5 0 0 1-1.5 1.5h-4zM19 12h4" />
      </g>
    </svg>
  </span>
</template>

<style scoped>
.plug {
  display: inline-flex;
  flex-shrink: 0;
  width: 17px;
  height: 17px;
  color: var(--faint);
  transition:
    color 0.3s ease,
    filter 0.3s ease;
}

svg {
  width: 100%;
  height: 100%;
  overflow: visible;
  fill: none;
  stroke: currentColor;
  stroke-width: 2;
  stroke-linecap: round;
  stroke-linejoin: round;
}

g {
  transition: transform 0.3s ease;
}

.plug:not(.on) .left {
  transform: translateX(-2.5px);
}

.plug:not(.on) .right {
  transform: translateX(2.5px);
}

.on {
  color: var(--accent);
  filter: drop-shadow(0 0 3px rgb(255 122 48 / 70%));
}

.warn {
  color: var(--warn);
}
</style>
