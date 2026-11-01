<script setup lang="ts">
import { computed, ref } from 'vue';
import { RouterLink } from 'vue-router';

import { listNetworks, pruneNetworks } from '../api/client';
import LoadState from '../components/LoadState.vue';
import NetworkCreate from '../components/NetworkCreate.vue';
import { useLiveReload } from '../composables/useEvents';
import { useLoad } from '../composables/useLoad';
import { useParams } from '../composables/params';
import { matches } from '../format';
import { currentUser } from '../session';

const { env } = useParams();
// Prunes are for admins.
const isAdmin = computed(() => !!currentUser.value?.admin);
const { data, error, reload } = useLoad(env, listNetworks);
// Docker events refresh the list, so changes made elsewhere show up by themselves.
useLiveReload(env, ['network'], reload);

const query = ref('');

const pruning = ref(false);
const pruneError = ref('');
const pruneResult = ref('');

async function onPrune() {
  if (!window.confirm('Remove every custom network no container uses?')) return;
  pruning.value = true;
  pruneError.value = '';
  pruneResult.value = '';
  try {
    const r = await pruneNetworks(env.value);
    pruneResult.value = r.deleted.length ? `Removed ${r.deleted.join(', ')}.` : 'Nothing to remove.';
    await reload();
  } catch (e) {
    pruneError.value = e instanceof Error ? e.message : String(e);
  } finally {
    pruning.value = false;
  }
}

const shown = computed(() => (data.value ?? []).filter((n) => matches(query.value, n.name, n.driver, n.id)));
</script>

<template>
  <h1>Networks</h1>
  <section class="card">
    <h2>Create</h2>
    <NetworkCreate :env="env" @created="reload" />
  </section>
  <section class="card">
    <div v-if="isAdmin" class="prune">
      <button type="button" class="danger" :disabled="pruning" @click="onPrune">Prune unused</button>
      <span v-if="pruneResult" class="muted" role="status">{{ pruneResult }}</span>
    </div>
    <p v-if="pruneError" class="error" role="alert">{{ pruneError }}</p>
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

.search {
  width: 100%;
  margin-bottom: 0.5rem;
}
</style>
