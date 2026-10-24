<script setup lang="ts">
import { computed, ref } from 'vue';
import { RouterLink } from 'vue-router';

import { listImages, pruneImages } from '../api/client';
import ImagePull from '../components/ImagePull.vue';
import LoadState from '../components/LoadState.vue';
import { useLiveReload } from '../composables/useEvents';
import { useLoad } from '../composables/useLoad';
import { useParams } from '../composables/params';
import { formatBytes, formatDate, matches, shortId } from '../format';

const { env } = useParams();
const { data, error, reload } = useLoad(env, listImages);
// Docker events refresh the list, so changes made elsewhere show up by themselves.
useLiveReload(env, ['image'], reload);

const query = ref('');
const danglingOnly = ref(false);

const pruning = ref(false);
const pruneError = ref('');
const pruneResult = ref('');

async function onPrune(all: boolean) {
  const what = all ? 'every image no container uses' : 'every untagged image';
  if (!window.confirm(`Remove ${what}? This cannot be undone.`)) return;
  pruning.value = true;
  pruneError.value = '';
  pruneResult.value = '';
  try {
    const r = await pruneImages(env.value, all);
    const n = r.deleted.length + r.untagged.length;
    pruneResult.value = n
      ? `Removed ${r.untagged.length} tags and ${r.deleted.length} images and layers, reclaimed ${formatBytes(r.space_reclaimed)}.`
      : 'Nothing to remove.';
    await reload();
  } catch (e) {
    pruneError.value = e instanceof Error ? e.message : String(e);
  } finally {
    pruning.value = false;
  }
}

const shown = computed(() =>
  (data.value ?? []).filter(
    (img) => (!danglingOnly.value || img.dangling) && matches(query.value, img.id, ...img.tags),
  ),
);
</script>

<template>
  <h1>Images</h1>
  <section class="card">
    <h2>Pull</h2>
    <ImagePull :env="env" @pulled="reload" />
  </section>
  <section class="card">
    <div class="prune">
      <button type="button" :disabled="pruning" @click="onPrune(false)">Prune untagged</button>
      <button type="button" class="danger" :disabled="pruning" @click="onPrune(true)">Prune unused</button>
      <span v-if="pruneResult" class="muted" role="status">{{ pruneResult }}</span>
    </div>
    <p v-if="pruneError" class="error" role="alert">{{ pruneError }}</p>
    <div class="filters">
      <input v-model="query" type="search" placeholder="Search tag or ID" aria-label="Search" />
      <label><input v-model="danglingOnly" type="checkbox" /> Untagged only</label>
    </div>
    <LoadState
      :loading="!data"
      :error="error"
      :ready="data !== null"
      :empty="shown.length === 0"
      :empty-text="data?.length ? 'No image matches.' : 'No images.'"
      @retry="reload"
    >
      <div class="table-wrap">
        <table>
          <thead>
            <tr>
              <th>Tags</th>
              <th>ID</th>
              <th>Size</th>
              <th class="wide-only">Created</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="img in shown" :key="img.id">
              <td class="break">
                <RouterLink :to="{ name: 'image', params: { env, id: img.id } }">
                  {{ img.tags.length ? img.tags.join(', ') : 'untagged' }}
                </RouterLink>
              </td>
              <td>
                <code>{{ shortId(img.id) }}</code>
              </td>
              <td>{{ formatBytes(img.size) }}</td>
              <td class="wide-only">{{ formatDate(img.created_at) }}</td>
            </tr>
          </tbody>
        </table>
      </div>
    </LoadState>
  </section>
</template>

<style scoped>
.card + .card {
  margin-top: 1rem;
}

.prune {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 0.5rem;
  margin-bottom: 0.75rem;
}

.filters {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 0.5rem 1rem;
  margin-bottom: 0.5rem;
}

.filters input[type='search'] {
  flex: 1 1 14rem;
}
</style>
