<!-- What on an environment wants a look: containers with an update available (from the
     last check), stopped containers and untagged images. -->
<script setup lang="ts">
import { computed, toRef } from 'vue';
import { RouterLink } from 'vue-router';

import { listContainers, listImages, listUpdates, type ContainerSummary } from '../api/client';
import { useLiveReload } from '../composables/useEvents';
import { useLoad } from '../composables/useLoad';
import { formatBytes } from '../format';

const props = defineProps<{ env: string }>();

// maxNamed bounds the containers named per group; the rest are counted.
const maxNamed = 5;

const env = toRef(props, 'env');
// A failed update list only hides that group, as with the update badges.
const { data, error, reload } = useLoad(env, async (id) => {
  const [containers, images, updates] = await Promise.all([
    listContainers(id),
    listImages(id),
    listUpdates(id).catch(() => []),
  ]);
  return { containers, images, updates };
});
useLiveReload(env, ['container', 'image'], reload);

const groups = computed(() => {
  if (!data.value) return null;
  const { containers, images, updates } = data.value;
  const available = new Set(
    updates.filter((u) => u.status === 'available').map((u) => `${u.image}\n${u.image_id}`),
  );
  const dangling = images.filter((i) => i.dangling);
  return {
    updates: containers.filter((c) => available.has(`${c.image}\n${c.image_id}`)),
    stopped: containers.filter((c) => c.state === 'exited' || c.state === 'dead'),
    dangling: dangling.length,
    danglingSize: dangling.reduce((n, i) => n + i.size, 0),
  };
});

const empty = computed(
  () =>
    groups.value && !groups.value.updates.length && !groups.value.stopped.length && !groups.value.dangling,
);

const named = (list: ContainerSummary[]) => list.slice(0, maxNamed);
const more = (list: ContainerSummary[]) => Math.max(0, list.length - maxNamed);
const plural = (n: number, one: string, many: string) => `${n} ${n === 1 ? one : many}`;
</script>

<template>
  <section class="card attention">
    <h2>Needs attention</h2>
    <p v-if="error" class="error" role="alert">{{ error }}</p>
    <p v-else-if="!groups" class="muted">Loading…</p>
    <p v-else-if="empty" class="muted">Nothing needs attention.</p>
    <ul v-else-if="groups">
      <li v-if="groups.updates.length" class="updates">
        <span class="lamp warn" aria-hidden="true" />
        <div>
          <RouterLink :to="{ name: 'containers', params: { env } }">{{
            plural(groups.updates.length, 'update available', 'updates available')
          }}</RouterLink>
          <p class="names">
            <template v-for="(c, i) in named(groups.updates)" :key="c.id"
              ><template v-if="i">, </template
              ><RouterLink :to="{ name: 'container', params: { env, id: c.id } }">{{
                c.name
              }}</RouterLink></template
            ><template v-if="more(groups.updates)"> and {{ more(groups.updates) }} more</template>
          </p>
        </div>
      </li>
      <li v-if="groups.stopped.length" class="stopped">
        <span class="lamp bad" aria-hidden="true" />
        <div>
          <RouterLink :to="{ name: 'containers', params: { env } }">{{
            plural(groups.stopped.length, 'stopped container', 'stopped containers')
          }}</RouterLink>
          <p class="names">
            <template v-for="(c, i) in named(groups.stopped)" :key="c.id"
              ><template v-if="i">, </template
              ><RouterLink :to="{ name: 'container', params: { env, id: c.id } }">{{
                c.name
              }}</RouterLink></template
            ><template v-if="more(groups.stopped)"> and {{ more(groups.stopped) }} more</template>
          </p>
        </div>
      </li>
      <li v-if="groups.dangling" class="dangling">
        <span class="lamp" aria-hidden="true" />
        <div>
          <RouterLink :to="{ name: 'images', params: { env } }">{{
            plural(groups.dangling, 'untagged image', 'untagged images')
          }}</RouterLink>
          <p class="names">{{ formatBytes(groups.danglingSize) }} could be reclaimed with a prune.</p>
        </div>
      </li>
    </ul>
  </section>
</template>

<style scoped>
ul {
  display: flex;
  flex-direction: column;
  gap: 0.85rem;
  margin: 0;
  padding: 0;
  list-style: none;
}

li {
  display: flex;
  gap: 0.75rem;
  align-items: baseline;
}

.names {
  margin: 0.15rem 0 0;
  font-size: 12.5px;
  color: var(--muted);
}
</style>
