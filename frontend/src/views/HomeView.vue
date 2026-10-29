<script setup lang="ts">
import { computed } from 'vue';
import { RouterLink } from 'vue-router';

import EnvironmentTile from '../components/EnvironmentTile.vue';
import LoadState from '../components/LoadState.vue';
import { useLoad } from '../composables/useLoad';
import { loadEnvironments } from '../environments';
import { currentUser } from '../session';

// The global dashboard: one tile per environment, each opening that environment.
const { data, error, reload } = useLoad(() => true, loadEnvironments);

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
    <RouterLink v-if="currentUser?.admin" class="button primary" to="/environments"
      >Add environment</RouterLink
    >
  </div>
  <LoadState
    :loading="!data"
    :error="error ? `Could not load the environments: ${error}` : ''"
    :ready="data !== null"
    :empty="data?.length === 0"
    empty-text="There are no environments yet."
    @retry="reload"
  >
    <div class="tiles">
      <EnvironmentTile v-for="env in data" :key="env.id" :env="env" />
      <RouterLink v-if="currentUser?.admin" to="/environments" class="add">
        <span class="plus" aria-hidden="true">+</span>
        <strong>Add environment</strong>
        <span class="muted">Connect a remote host with the nudge agent</span>
      </RouterLink>
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
  color: var(--text);
  text-align: center;
  text-decoration: none;
  border: 1px dashed var(--border-strong);
  border-radius: var(--radius-lg);
  transition: border-color 0.15s ease;
}

.add:hover {
  transition-duration: var(--hover-in);
  color: var(--text);
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
