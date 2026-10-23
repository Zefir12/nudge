<script setup lang="ts">
import { onMounted, ref } from 'vue';

import { getHealth, type Health } from '../api/client';

const health = ref<Health | null>(null);
const error = ref('');

onMounted(async () => {
  try {
    health.value = await getHealth();
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e);
  }
});
</script>

<template>
  <h1>Dashboard</h1>
  <section class="card">
    <h2>Server</h2>
    <p v-if="error" class="error">Unreachable: {{ error }}</p>
    <p v-else-if="health">
      Status <strong>{{ health.status }}</strong> · version <code>{{ health.version }}</code>
    </p>
    <p v-else class="muted">Loading…</p>
  </section>
</template>
