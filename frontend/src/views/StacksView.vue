<script setup lang="ts">
import { computed, ref } from 'vue';
import { RouterLink } from 'vue-router';

import { listContainers, listStacks } from '../api/client';
import LoadState from '../components/LoadState.vue';
import UpdateBadge from '../components/UpdateBadge.vue';
import { useLiveReload } from '../composables/useEvents';
import { useLoad } from '../composables/useLoad';
import { useParams } from '../composables/params';
import { useUpdates } from '../composables/useUpdates';
import { matches } from '../format';

const { env } = useParams();
const { data, error, reload } = useLoad(env, listStacks);
// A stack shows an update when one of its containers runs an image with one.
const containers = useLoad(env, listContainers);
// Container events change the counts and the images.
useLiveReload(env, ['container'], () => Promise.all([reload(), containers.reload()]));
const { updateFor } = useUpdates(env);
const stackUpdates = computed(() => {
  const out = new Map<string, NonNullable<ReturnType<typeof updateFor>>>();
  for (const c of containers.data.value ?? []) {
    const project = c.labels['com.docker.compose.project'];
    const u = updateFor(c);
    if (project && u && !out.has(project)) out.set(project, u);
  }
  return out;
});

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
                <UpdateBadge v-if="stackUpdates.get(s.name)" :update="stackUpdates.get(s.name)!" />
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
