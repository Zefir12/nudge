<script setup lang="ts">
import { computed, onMounted, ref } from 'vue';
import { useRoute, useRouter } from 'vue-router';

import { environments, loadEnvironments, rememberEnvironment } from '../environments';
import EnvStatus from './EnvStatus.vue';

const route = useRoute();
const router = useRouter();
const error = ref('');

const current = computed(() => (typeof route.params.env === 'string' ? route.params.env : ''));
const currentEnv = computed(() => environments.value?.find((e) => String(e.id) === current.value));

// A detail page has no counterpart in another environment, so switching goes to its list.
const listOf: Record<string, string> = {
  container: 'containers',
  image: 'images',
  volume: 'volumes',
  network: 'networks',
};

function onChange(e: Event) {
  const env = (e.target as HTMLSelectElement).value;
  rememberEnvironment(env);
  const name =
    typeof route.name === 'string' && route.params.env ? (listOf[route.name] ?? route.name) : 'env';
  void router.push({ name, params: { env } });
}

onMounted(async () => {
  try {
    await loadEnvironments();
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e);
  }
});
</script>

<template>
  <div class="switcher">
    <label class="field">
      <span class="muted">Environment</span>
      <select :value="current" :disabled="!environments" @change="onChange">
        <option v-if="!current" value="" disabled>Choose…</option>
        <option
          v-else-if="environments && !environments.some((e) => String(e.id) === current)"
          :value="current"
          disabled
        >
          Unknown environment
        </option>
        <option v-for="env in environments ?? []" :key="env.id" :value="String(env.id)">
          {{ env.name }}{{ env.online ? '' : ' (offline)' }}
        </option>
      </select>
    </label>
    <EnvStatus v-if="currentEnv" :env="currentEnv" />
    <span v-if="error" class="error">{{ error }}</span>
  </div>
</template>

<style scoped>
.switcher,
.field {
  display: flex;
  flex-direction: column;
  gap: 0.25rem;
  font-size: 0.9rem;
}
</style>
