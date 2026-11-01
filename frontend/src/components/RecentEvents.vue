<!-- The environment's latest Docker events since the page opened; the Events page has all. -->
<script setup lang="ts">
import { ref, toRef, watch } from 'vue';
import { RouterLink } from 'vue-router';

import type { DockerEvent } from '../api/client';
import { useEvents } from '../composables/useEvents';
import { eventLabel, eventPage } from '../eventLinks';

const props = defineProps<{ env: string }>();

// maxEvents is how many events the panel shows; older ones drop off the end.
const maxEvents = 8;

const env = toRef(props, 'env');
const events = ref<DockerEvent[]>([]);
// Exec events come with every terminal and health check and say little here.
const { error } = useEvents(env, [], (e) => {
  if (e.action.startsWith('exec_')) return;
  events.value = [e, ...events.value].slice(0, maxEvents);
});
watch(env, () => {
  events.value = [];
});

const time = (iso: string) => new Date(iso).toLocaleTimeString();
</script>

<template>
  <section class="card recent">
    <div class="title">
      <h2>Recent events</h2>
      <RouterLink :to="{ name: 'events', params: { env } }">All events</RouterLink>
    </div>
    <p v-if="error" class="error" role="alert">{{ error }} Reconnecting…</p>
    <p v-if="events.length === 0" class="muted">No events since the page was opened.</p>
    <ul v-else>
      <li v-for="(e, i) in events" :key="`${e.time}-${e.id}-${e.action}-${i}`">
        <span class="time">{{ time(e.time) }}</span>
        <span class="what">
          {{ e.type }} <code>{{ e.action }}</code>
          <RouterLink v-if="eventPage(e)" :to="{ name: eventPage(e), params: { env, id: e.id } }">{{
            eventLabel(e)
          }}</RouterLink>
          <template v-else>{{ eventLabel(e) }}</template>
        </span>
      </li>
    </ul>
  </section>
</template>

<style scoped>
.title {
  display: flex;
  align-items: baseline;
  justify-content: space-between;
  gap: 1rem;
}

.title a {
  font-size: 12.5px;
}

ul {
  display: flex;
  flex-direction: column;
  gap: 0.45rem;
  margin: 0;
  padding: 0;
  list-style: none;
}

li {
  display: flex;
  gap: 0.75rem;
  font-size: 13px;
}

.time {
  flex: none;
  font-family: var(--mono);
  font-size: 12px;
  color: var(--muted);
}

.what {
  min-width: 0;
  overflow-wrap: anywhere;
}
</style>
