<script setup lang="ts">
import { computed, ref } from 'vue';
import { RouterLink, useRouter } from 'vue-router';

import { getImage, removeImage } from '../api/client';
import FoldCard from '../components/FoldCard.vue';
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

const router = useRouter();
const force = ref(false);
const removing = ref(false);
const removeError = ref('');

async function onRemove() {
  if (!img.value) return;
  const name = img.value.tags[0] ?? shortId(img.value.id);
  let msg = `Remove the image "${name}"? This cannot be undone.`;
  if (force.value) msg += ' All its tags are removed, even if stopped containers use it.';
  if (!window.confirm(msg)) return;
  removing.value = true;
  removeError.value = '';
  try {
    // By ID with force, so every tag goes; else by the URL's reference (a tag only
    // untags it while other tags remain).
    await removeImage(env.value, force.value ? img.value.id : id.value, force.value);
    await router.push({ name: 'images', params: { env: env.value } });
  } catch (e) {
    removeError.value = e instanceof Error ? e.message : String(e);
  } finally {
    removing.value = false;
  }
}

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
      <div class="actions">
        <button type="button" class="danger" :disabled="removing" @click="onRemove">Remove</button>
        <label>
          <input v-model="force" type="checkbox" :disabled="removing" />
          force (all tags, even if stopped containers use it)
        </label>
      </div>
      <p v-if="removeError" class="error" role="alert">{{ removeError }}</p>
      <FoldCard fold-id="image.overview">
        <template #title>Overview</template>
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
      </FoldCard>
      <FoldCard fold-id="image.config">
        <template #title>Config</template>
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
      </FoldCard>
      <FoldCard fold-id="image.labels">
        <template #title>Labels</template>
        <p v-if="labels.length === 0" class="muted">No labels.</p>
        <KeyValues v-else :rows="labels" mono />
      </FoldCard>
      <FoldCard fold-id="image.layers">
        <template #title>Layers ({{ img.layers.length }})</template>
        <ol>
          <li v-for="l in img.layers" :key="l">
            <code class="break">{{ l }}</code>
          </li>
        </ol>
      </FoldCard>
    </template>
  </LoadState>
</template>

<style scoped>
.actions {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 0.5rem;
  margin-bottom: 1rem;
}

.actions label {
  display: inline-flex;
  align-items: center;
  gap: 0.3rem;
}

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
