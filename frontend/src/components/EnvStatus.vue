<script setup lang="ts">
import { computed } from 'vue';

import type { Environment } from '../api/client';
import { formatDate } from '../format';

// EnvStatus shows whether an environment's Docker host answers, and when it last did.
const props = defineProps<{ env: Environment }>();

const status = computed(() => {
  if (props.env.online) return { label: 'online', tone: 'good' };
  if (props.env.kind === 'agent' && !props.env.agent?.enrolled)
    return { label: 'waiting for agent', tone: 'warn' };
  return { label: 'offline', tone: 'bad' };
});
</script>

<template>
  <span class="status">
    <span class="badge" :class="status.tone">{{ status.label }}</span>
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
  gap: 0.25rem 0.5rem;
}

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

.muted {
  font-size: 0.85em;
}
</style>
