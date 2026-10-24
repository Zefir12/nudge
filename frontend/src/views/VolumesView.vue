<script setup lang="ts">
import { computed, ref } from 'vue';
import { RouterLink } from 'vue-router';

import { listVolumes } from '../api/client';
import LoadState from '../components/LoadState.vue';
import { useLoad } from '../composables/useLoad';
import { useParams } from '../composables/params';
import { formatDate, matches } from '../format';

const { env } = useParams();
const { data, error, reload } = useLoad(env, listVolumes);

const query = ref('');
const shown = computed(() => (data.value ?? []).filter((v) => matches(query.value, v.name, v.driver)));
</script>

<template>
  <h1>Volumes</h1>
  <section class="card">
    <input
      v-model="query"
      class="search"
      type="search"
      placeholder="Search name or driver"
      aria-label="Search"
    />
    <LoadState
      :loading="!data"
      :error="error"
      :ready="data !== null"
      :empty="shown.length === 0"
      :empty-text="data?.length ? 'No volume matches.' : 'No volumes.'"
      @retry="reload"
    >
      <div class="table-wrap">
        <table>
          <thead>
            <tr>
              <th>Name</th>
              <th>Driver</th>
              <th class="wide-only">Mountpoint</th>
              <th class="wide-only">Created</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="v in shown" :key="v.name">
              <td class="break">
                <RouterLink :to="{ name: 'volume', params: { env, id: v.name } }">{{ v.name }}</RouterLink>
              </td>
              <td>{{ v.driver }}</td>
              <td class="wide-only break">
                <code>{{ v.mountpoint }}</code>
              </td>
              <td class="wide-only">{{ formatDate(v.created_at) }}</td>
            </tr>
          </tbody>
        </table>
      </div>
    </LoadState>
  </section>
</template>

<style scoped>
.search {
  width: 100%;
  margin-bottom: 0.5rem;
}
</style>
