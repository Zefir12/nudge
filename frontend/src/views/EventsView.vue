<script setup lang="ts">
import { computed, ref, watch } from 'vue';
import { RouterLink } from 'vue-router';

import type { DockerEvent } from '../api/client';
import { useEvents } from '../composables/useEvents';
import { useParams } from '../composables/params';
import { formatDate, matches, shortId } from '../format';

// maxEvents bounds the events kept on the page; older ones drop off the end.
const maxEvents = 500;

const { env } = useParams();
const events = ref<DockerEvent[]>([]);
const type = ref('');
const query = ref('');
const paused = ref(false);

const { error } = useEvents(env, [], (e) => {
  if (paused.value) return;
  events.value = [e, ...events.value].slice(0, maxEvents);
});
watch(env, () => {
  events.value = [];
});

const shown = computed(() =>
  events.value.filter(
    (e) => (!type.value || e.type === type.value) && matches(query.value, e.action, e.name, e.id),
  ),
);

// Only containers, images, volumes and networks have pages to link to.
const pages: Record<string, string> = {
  container: 'container',
  image: 'image',
  volume: 'volume',
  network: 'network',
};

function linkable(e: DockerEvent) {
  return pages[e.type] && e.action !== 'destroy' && e.action !== 'delete' && e.action !== 'untag';
}

const label = (e: DockerEvent) =>
  e.name || (e.type === 'container' || e.type === 'network' ? shortId(e.id) : e.id);
</script>

<template>
  <h1>Events</h1>
  <p class="muted">Docker events on this environment since the page was opened, newest first.</p>

  <section class="card">
    <div class="filters">
      <select v-model="type" aria-label="Type">
        <option value="">All types</option>
        <option value="container">Containers</option>
        <option value="image">Images</option>
        <option value="volume">Volumes</option>
        <option value="network">Networks</option>
        <option value="daemon">Daemon</option>
        <option value="plugin">Plugins</option>
      </select>
      <input v-model="query" type="search" placeholder="Search action, name or ID" aria-label="Search" />
      <button type="button" @click="paused = !paused">{{ paused ? 'Resume' : 'Pause' }}</button>
      <button type="button" :disabled="events.length === 0" @click="events = []">Clear</button>
    </div>
    <p v-if="error" class="error" role="alert">{{ error }} Reconnecting…</p>
    <p v-if="paused" class="muted">Paused: new events are not recorded.</p>

    <p v-if="shown.length === 0" class="muted">
      {{ events.length ? 'No event matches.' : 'Waiting for events…' }}
    </p>
    <div v-else class="table-wrap">
      <table>
        <thead>
          <tr>
            <th>Time</th>
            <th>Type</th>
            <th>Action</th>
            <th>Object</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="(e, i) in shown" :key="`${e.time}-${e.id}-${e.action}-${i}`">
            <td>{{ formatDate(e.time) }}</td>
            <td>{{ e.type }}</td>
            <td>
              <code>{{ e.action }}</code>
            </td>
            <td class="break">
              <RouterLink v-if="linkable(e)" :to="{ name: pages[e.type], params: { env, id: e.id } }">{{
                label(e)
              }}</RouterLink>
              <template v-else>{{ label(e) }}</template>
            </td>
          </tr>
        </tbody>
      </table>
    </div>
  </section>
</template>

<style scoped>
.filters {
  display: flex;
  flex-wrap: wrap;
  gap: 0.5rem;
  margin-bottom: 0.5rem;
}

.filters input {
  flex: 1 1 14rem;
}
</style>
