<script setup lang="ts">
import { computed, ref } from 'vue';
import { RouterLink } from 'vue-router';

import { listNetworks } from '../api/client';
import LoadState from '../components/LoadState.vue';
import { useLoad } from '../composables/useLoad';
import { useParams } from '../composables/params';
import { matches } from '../format';

const { env } = useParams();
const { data, error, reload } = useLoad(env, listNetworks);

const query = ref('');
const shown = computed(() => (data.value ?? []).filter((n) => matches(query.value, n.name, n.driver, n.id)));
</script>

<template>
  <h1>Networks</h1>
  <section class="card">
    <input
      v-model="query"
      class="search"
      type="search"
      placeholder="Search name, driver or ID"
      aria-label="Search"
    />
    <LoadState
      :loading="!data"
      :error="error"
      :ready="data !== null"
      :empty="shown.length === 0"
      :empty-text="data?.length ? 'No network matches.' : 'No networks.'"
      @retry="reload"
    >
      <div class="table-wrap">
        <table>
          <thead>
            <tr>
              <th>Name</th>
              <th>Driver</th>
              <th class="wide-only">Scope</th>
              <th class="wide-only">Subnet</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="n in shown" :key="n.id">
              <td class="break">
                <RouterLink :to="{ name: 'network', params: { env, id: n.id } }">{{ n.name }}</RouterLink>
                <span v-if="n.internal" class="muted"> · internal</span>
              </td>
              <td>{{ n.driver }}</td>
              <td class="wide-only">{{ n.scope }}</td>
              <td class="wide-only">
                {{
                  n.ipam
                    .map((c) => c.subnet)
                    .filter(Boolean)
                    .join(', ') || '—'
                }}
              </td>
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
