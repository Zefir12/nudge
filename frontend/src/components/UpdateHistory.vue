<script setup lang="ts">
import { listUpdateHistory, type UpdateRecord } from '../api/client';
import { useLoad } from '../composables/useLoad';
import { formatDate, shortId } from '../format';

// UpdateHistory lists the updates made to a container or stack through nudge, with the
// image each ran before: what a rollback by hand needs. It shows nothing until there is one.
const props = defineProps<{ env: string; kind: 'container' | 'stack'; target: string }>();

const { data, reload } = useLoad(
  () => [props.env, props.kind, props.target] as const,
  ([env, kind, target]) => listUpdateHistory(env, { kind, target }),
);
defineExpose({ reload });

const image = (id: string, digest: string) => (digest ? `${shortId(id)} (${digest})` : shortId(id));
const title = (u: UpdateRecord) => `Before: ${u.old_image_id}\nAfter: ${u.new_image_id}`;
</script>

<template>
  <section v-if="data && data.length" class="card">
    <h2>Updates</h2>
    <div class="table-wrap">
      <table>
        <thead>
          <tr>
            <th>Time</th>
            <th v-if="kind === 'stack'">Service</th>
            <th>Image</th>
            <th>Before</th>
            <th>After</th>
            <th>User</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="u in data" :key="u.id" :title="title(u)">
            <td>{{ formatDate(u.time) }}</td>
            <td v-if="kind === 'stack'">{{ u.service }}</td>
            <td class="break">{{ u.image }}</td>
            <td class="mono break">{{ image(u.old_image_id, u.old_digest) }}</td>
            <td class="mono break">{{ image(u.new_image_id, u.new_digest) }}</td>
            <td>{{ u.user }}</td>
          </tr>
        </tbody>
      </table>
    </div>
  </section>
</template>

<style scoped>
.mono {
  font-family: var(--mono);
  font-size: 0.9em;
}
</style>
