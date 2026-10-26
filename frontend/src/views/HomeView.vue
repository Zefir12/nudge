<script setup lang="ts">
import { onMounted, ref } from 'vue';
import { useRouter } from 'vue-router';

import { defaultEnvironment, loadEnvironments } from '../environments';

// The start page opens the environment used last, or the first one.
const router = useRouter();
const error = ref('');
const none = ref(false);

onMounted(async () => {
  try {
    const env = defaultEnvironment(await loadEnvironments());
    if (env) await router.replace({ name: 'env', params: { env: String(env.id) } });
    else none.value = true;
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e);
  }
});
</script>

<template>
  <h1>Dashboard</h1>
  <p v-if="error" class="error" role="alert">Could not load the environments: {{ error }}</p>
  <p v-else-if="none" class="muted">There are no environments yet.</p>
  <p v-else class="muted">Loading…</p>
</template>
