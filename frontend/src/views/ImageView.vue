<script setup lang="ts">
import { computed } from 'vue';
import { RouterLink } from 'vue-router';

import { getImage } from '../api/client';
import KeyValues from '../components/KeyValues.vue';
import LoadState from '../components/LoadState.vue';
import { useLoad } from '../composables/useLoad';
import { useParams } from '../composables/params';
import { formatBytes, formatDate, shortId } from '../format';

const { env, id } = useParams();
const {
  data: img,
  error,
  reload,
} = useLoad(
  () => [env.value, id.value] as const,
  ([e, i]) => getImage(e, i),
);

const labels = computed(() =>
  Object.entries(img.value?.config.labels ?? {}).sort(([a], [b]) => a.localeCompare(b)),
);
const platform = computed(() =>
  img.value ? [img.value.os, img.value.architecture, img.value.variant].filter(Boolean).join('/') : '',
);
</script>

<template>
  <p><RouterLink :to="{ name: 'images', params: { env } }">← Images</RouterLink></p>
  <LoadState :loading="!img" :error="error" :ready="img !== null" @retry="reload">
    <template v-if="img">
      <h1 class="break">{{ img.tags[0] ?? shortId(img.id) }}</h1>
      <section class="card">
        <h2>Overview</h2>
        <KeyValues
          :rows="[
            ['ID', img.id],
            ['Tags', img.tags.join(', ')],
            ['Digests', img.digests.join(', ')],
            ['Created', formatDate(img.created_at)],
            ['Size', formatBytes(img.size)],
            ['Platform', platform],
            ['Author', img.author],
            ['Comment', img.comment],
          ]"
        />
      </section>
      <section class="card">
        <h2>Config</h2>
        <KeyValues
          :rows="[
            ['Entrypoint', img.config.entrypoint.join(' ')],
            ['Command', img.config.cmd.join(' ')],
            ['Working dir', img.config.working_dir],
            ['User', img.config.user],
            ['Exposed ports', img.config.exposed_ports.join(', ')],
            ['Volumes', img.config.volumes.join(', ')],
          ]"
        />
        <h2 class="sub">Environment</h2>
        <p v-if="img.config.env.length === 0" class="muted">No variables.</p>
        <ul v-else>
          <li v-for="v in img.config.env" :key="v">
            <code class="break">{{ v }}</code>
          </li>
        </ul>
      </section>
      <section class="card">
        <h2>Labels</h2>
        <p v-if="labels.length === 0" class="muted">No labels.</p>
        <KeyValues v-else :rows="labels" mono />
      </section>
      <section class="card">
        <h2>Layers ({{ img.layers.length }})</h2>
        <ol>
          <li v-for="l in img.layers" :key="l">
            <code class="break">{{ l }}</code>
          </li>
        </ol>
      </section>
    </template>
  </LoadState>
</template>

<style scoped>
.card + .card {
  margin-top: 1rem;
}

.sub {
  margin-top: 1rem;
}

ul {
  margin: 0;
  padding: 0;
  list-style: none;
}

ol {
  margin: 0;
  padding-left: 1.5rem;
}
</style>
