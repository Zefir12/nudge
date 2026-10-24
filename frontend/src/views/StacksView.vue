<script setup lang="ts">
import { computed, ref } from 'vue';
import { RouterLink } from 'vue-router';

import { listStacks } from '../api/client';
import LoadState from '../components/LoadState.vue';
import { useLiveReload } from '../composables/useEvents';
import { useLoad } from '../composables/useLoad';
import { useParams } from '../composables/params';
import { matches } from '../format';

const { env } = useParams();
const { data, error, reload } = useLoad(env, listStacks);
// Container events change the counts.
useLiveReload(env, ['container'], reload);

const query = ref('');
const shown = computed(() => (data.value ?? []).filter((s) => matches(query.value, s.name)));

const status = (s: { containers: number; running: number }) => {
  if (s.containers === 0) return { text: 'down', tone: 'muted' };
  if (s.running === s.containers) return { text: 'running', tone: 'good' };
  if (s.running === 0) return { text: 'stopped', tone: 'bad' };
  return { text: 'partly running', tone: 'warn' };
};
</script>

<template>
  <h1>Stacks</h1>
  <section class="card">
    <div class="toolbar">
      <RouterLink class="button" :to="{ name: 'stack-new', params: { env } }">New stack</RouterLink>
      <input v-model="query" class="search" type="search" placeholder="Search name" aria-label="Search" />
    </div>
    <LoadState
      :loading="!data"
      :error="error"
      :ready="data !== null"
      :empty="shown.length === 0"
      :empty-text="data?.length ? 'No stack matches.' : 'No stacks yet.'"
      @retry="reload"
    >
      <div class="table-wrap">
        <table>
          <thead>
            <tr>
              <th>Name</th>
              <th>Status</th>
              <th>Containers</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="s in shown" :key="s.name">
              <td class="break">
                <RouterLink :to="{ name: 'stack', params: { env, id: s.name } }">{{ s.name }}</RouterLink>
              </td>
              <td>
                <span class="status" :class="status(s).tone">{{ status(s).text }}</span>
              </td>
              <td>{{ s.running }} / {{ s.containers }} running</td>
            </tr>
          </tbody>
        </table>
      </div>
    </LoadState>
  </section>
</template>

<style scoped>
.toolbar {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 0.5rem;
  margin-bottom: 0.75rem;
}

.search {
  flex: 1;
  min-width: 12rem;
}

.status.good {
  color: var(--ok);
}

.status.warn {
  color: var(--warn);
}

.status.bad {
  color: var(--danger);
}

.status.muted {
  color: var(--muted);
}
</style>
