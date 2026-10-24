<script setup lang="ts">
import { computed, ref } from 'vue';
import { RouterLink } from 'vue-router';

import { listImages } from '../api/client';
import LoadState from '../components/LoadState.vue';
import { useLoad } from '../composables/useLoad';
import { useParams } from '../composables/params';
import { formatBytes, formatDate, matches, shortId } from '../format';

const { env } = useParams();
const { data, error, reload } = useLoad(env, listImages);

const query = ref('');
const danglingOnly = ref(false);

const shown = computed(() =>
  (data.value ?? []).filter(
    (img) => (!danglingOnly.value || img.dangling) && matches(query.value, img.id, ...img.tags),
  ),
);
</script>

<template>
  <h1>Images</h1>
  <section class="card">
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
