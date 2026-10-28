<script setup lang="ts">
import { computed, onMounted, ref } from 'vue';
import { useRoute, useRouter } from 'vue-router';

import { envIcon } from '../envIcon';
import { environments, loadEnvironments, rememberEnvironment } from '../environments';

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
      <span class="pick" :class="{ 'has-icon': currentEnv }">
        <svg v-if="currentEnv" viewBox="0 0 24 24" aria-hidden="true">
          <path :d="envIcon(currentEnv.name)" />
        </svg>
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
      </span>
    </label>
    <span v-if="error" class="error">{{ error }}</span>
  </div>
</template>

<style scoped>
.switcher {
  display: flex;
  flex-direction: column;
  gap: 0.4rem;
}

.field {
  display: flex;
  flex-direction: column;
  gap: 0.2rem;
}

.field > span {
  font-size: 11px;
  font-weight: 600;
  text-transform: uppercase;
  letter-spacing: 0.12em;
}

.pick {
  position: relative;
  display: block;
}

.pick svg {
  position: absolute;
  top: 50%;
  left: 0.55rem;
  width: 16px;
  height: 16px;
  color: var(--accent);
  fill: none;
  stroke: currentColor;
  stroke-width: 1.8;
  stroke-linecap: round;
  stroke-linejoin: round;
  transform: translateY(-50%);
  pointer-events: none;
}

.has-icon select {
  padding-left: 2rem;
}

select {
  width: 100%;
  font-family: var(--font-display);
  font-size: 14px;
  font-weight: 600;
}
</style>
