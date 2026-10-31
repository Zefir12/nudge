<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue';

import AddEnvironment from '../components/AddEnvironment.vue';
import EnvironmentTile from '../components/EnvironmentTile.vue';
import LoadState from '../components/LoadState.vue';
import SettingsDialog from '../components/SettingsDialog.vue';
import { useLoad } from '../composables/useLoad';
import { loadEnvironments } from '../environments';
import { currentUser } from '../session';

// The global dashboard: one tile per environment, each opening that environment. Admins
// add environments in a dialog and manage each one from its tile's gear.
const { data, error, reload } = useLoad(() => true, loadEnvironments);
const adding = ref(false);

// refreshMs is how often the list is asked for again, so an agent coming online or going
// offline, a new agent version and added or removed environments show without a reload.
// The tiles are keyed by ID, so they (and any dialog open on one) stay mounted. A failed
// refresh keeps the tiles as they were; the next one tries again.
const refreshMs = 30_000;
async function refresh() {
  if (data.value === null || document.visibilityState === 'hidden') return;
  try {
    data.value = await loadEnvironments();
  } catch {
    // Keep the last list.
  }
}
const onVisibility = () => void refresh();
let timer: ReturnType<typeof setInterval> | undefined;
onMounted(() => {
  timer = setInterval(() => void refresh(), refreshMs);
  document.addEventListener('visibilitychange', onVisibility);
});
onUnmounted(() => {
  clearInterval(timer);
  document.removeEventListener('visibilitychange', onVisibility);
});

const summary = computed(() => {
  const envs = data.value ?? [];
  const online = envs.filter((e) => e.online).length;
  return `${envs.length} ${envs.length === 1 ? 'host' : 'hosts'} · ${online} online`;
});
</script>

<template>
  <div class="head">
    <div>
      <span class="eyebrow">Overview</span>
      <h1>Environments</h1>
      <p v-if="data" class="muted">{{ summary }}</p>
    </div>
    <button v-if="currentUser?.admin" type="button" class="primary" @click="adding = true">
      Add environment
    </button>
  </div>
  <SettingsDialog v-if="adding" title="Add environment" @close="adding = false">
    <AddEnvironment @created="reload" @done="adding = false" />
  </SettingsDialog>
  <LoadState
    :loading="!data"
    :error="error ? `Could not load the environments: ${error}` : ''"
    :ready="data !== null"
    :empty="data?.length === 0"
    empty-text="There are no environments yet."
    @retry="reload"
  >
    <div class="tiles">
      <EnvironmentTile v-for="env in data" :key="env.id" :env="env" @changed="reload" />
      <button v-if="currentUser?.admin" type="button" class="add" @click="adding = true">
        <span class="plus" aria-hidden="true">+</span>
        <strong>Add environment</strong>
        <span class="muted">Connect a remote host with the nudge agent</span>
      </button>
    </div>
  </LoadState>
</template>

<style scoped>
.head {
  display: flex;
  flex-wrap: wrap;
  align-items: flex-end;
  justify-content: space-between;
  gap: 1rem;
  margin-bottom: 1.75rem;
}

h1 {
  margin: 0.15rem 0 0.25rem;
}

.head p {
  margin: 0;
}

.tiles {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(min(34rem, 100%), 1fr));
  gap: 1.25rem;
}

.add {
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  gap: 0.5rem;
  min-height: 260px;
  padding: 1.5rem;
  font-size: 13px;
  font-weight: 400;
  color: var(--text);
  text-align: center;
  background: transparent;
  border: 1px dashed var(--border-strong);
  border-radius: var(--radius-lg);
  transition: border-color 0.15s ease;
}

.add:hover:not(:disabled) {
  transition-duration: var(--hover-in);
  color: var(--text);
  background: transparent;
  border-color: var(--accent-border);
}

.add strong {
  font-family: var(--font-display);
  font-size: 17px;
}

.plus {
  display: grid;
  place-items: center;
  width: 44px;
  height: 44px;
  font-size: 22px;
  color: var(--accent-strong);
  background: var(--accent-soft);
  border: 1px solid var(--accent-border);
  border-radius: var(--radius-lg);
}
</style>
