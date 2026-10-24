<script setup lang="ts">
import { computed } from 'vue';

import type { ImageUpdate } from '../api/client';
import { formatDate } from '../format';

const props = defineProps<{ update: ImageUpdate }>();

const title = computed(
  () =>
    `A newer ${props.update.image} is in the registry (checked ${formatDate(props.update.checked_at)}).\n` +
    `Running: ${props.update.local_digest}\nRegistry: ${props.update.remote_digest}`,
);
</script>

<template>
  <span class="update" :title="title">update</span>
</template>

<style scoped>
.update {
  display: inline-block;
  margin-left: 0.4rem;
  padding: 0 0.4rem;
  border-radius: 999px;
  background: var(--accent-soft);
  color: var(--accent);
  font-size: 0.8em;
  white-space: nowrap;
}
</style>
