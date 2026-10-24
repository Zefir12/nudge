<script setup lang="ts">
// LoadState shows loading, error and empty states, and the slot once there is data.
defineProps<{
  loading: boolean;
  error: string;
  // ready is true once data arrived; reloads keep showing the old data.
  ready: boolean;
  empty?: boolean;
  emptyText?: string;
}>();
defineEmits<{ retry: [] }>();
</script>

<template>
  <div v-if="error" class="failed" role="alert">
    <p class="error">{{ error }}</p>
    <button type="button" @click="$emit('retry')">Retry</button>
  </div>
  <p v-else-if="!ready" class="muted">Loading…</p>
  <p v-else-if="empty" class="muted">{{ emptyText ?? 'Nothing here.' }}</p>
  <slot v-else />
</template>

<style scoped>
.failed {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 0.75rem;
}

.failed p {
  margin: 0;
}
</style>
