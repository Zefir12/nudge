<script setup lang="ts">
import { computed } from 'vue';

// StateBadge shows a container's state, and its health when it has a health check.
const props = defineProps<{ state: string; health?: string }>();

const tone = computed(() => {
  if (props.health === 'unhealthy') return 'bad';
  switch (props.state) {
    case 'running':
      return 'good';
    case 'exited':
    case 'dead':
      return 'bad';
    default:
      return 'warn';
  }
});
</script>

<template>
  <span class="badge" :class="tone"
    >{{ state }}<template v-if="health"> · {{ health }}</template></span
  >
</template>

<style scoped>
.badge {
  display: inline-block;
  padding: 0.05rem 0.5rem;
  font-size: 0.85em;
  white-space: nowrap;
  border: 1px solid currentColor;
  border-radius: 999px;
}

.good {
  color: var(--ok);
}

.warn {
  color: var(--warn);
}

.bad {
  color: var(--danger);
}
</style>
