<script setup lang="ts">
import { computed } from 'vue';

import type { Environment } from '../api/client';
import { formatDate } from '../format';

// EnvStatus shows whether an environment's Docker host answers, and when it last did.
const props = defineProps<{ env: Environment }>();

const status = computed(() => {
  if (props.env.online) return { label: 'online', tone: 'good', lamp: 'on' };
  if (props.env.kind === 'agent' && !props.env.agent?.enrolled)
    return { label: 'waiting for agent', tone: 'warn', lamp: 'warn' };
  return { label: 'offline', tone: 'bad', lamp: '' };
});
</script>

<template>
  <span class="status">
    <span class="badge" :class="status.tone">
      <span class="lamp" :class="status.lamp" aria-hidden="true" />{{ status.label }}
    </span>
    <span v-if="!env.online && env.last_seen_at" class="muted">
      last seen {{ formatDate(env.last_seen_at) }}
    </span>
  </span>
</template>

<style scoped>
.status {
  display: inline-flex;
  flex-wrap: wrap;
  align-items: baseline;
  gap: 0.25rem 0.6rem;
}

.badge {
  display: inline-flex;
  align-items: center;
  gap: 0.45rem;
  font-size: 0.9em;
  font-weight: 500;
  white-space: nowrap;
}

.good {
  color: var(--ok-text);
}

.warn {
  color: var(--warn);
}

.bad {
  color: var(--muted);
}

.muted {
  font-size: 0.85em;
}
</style>
