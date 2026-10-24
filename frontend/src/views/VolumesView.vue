<script setup lang="ts">
import { computed, ref } from 'vue';
import { RouterLink } from 'vue-router';

import { listVolumes, pruneVolumes } from '../api/client';
import LoadState from '../components/LoadState.vue';
import VolumeCreate from '../components/VolumeCreate.vue';
import { useLoad } from '../composables/useLoad';
import { useParams } from '../composables/params';
import { formatBytes, formatDate, matches } from '../format';

const { env } = useParams();
const { data, error, reload } = useLoad(env, listVolumes);

const query = ref('');

const pruning = ref(false);
const pruneError = ref('');
const pruneResult = ref('');

async function onPrune(all: boolean) {
  const what = all
    ? 'every volume no container uses, named ones too'
    : 'every anonymous volume no container uses';
  if (!window.confirm(`Remove ${what}? Their data is lost.`)) return;
  pruning.value = true;
  pruneError.value = '';
  pruneResult.value = '';
  try {
    const r = await pruneVolumes(env.value, all);
    pruneResult.value = r.deleted.length
      ? `Removed ${r.deleted.length} volumes, reclaimed ${formatBytes(r.space_reclaimed)}.`
      : 'Nothing to remove.';
    await reload();
  } catch (e) {
    pruneError.value = e instanceof Error ? e.message : String(e);
  } finally {
    pruning.value = false;
  }
}

const shown = computed(() => (data.value ?? []).filter((v) => matches(query.value, v.name, v.driver)));
</script>

<template>
  <h1>Volumes</h1>
  <section class="card">
    <h2>Create</h2>
    <VolumeCreate :env="env" @created="reload" />
  </section>
  <section class="card">
    <div class="prune">
      <button type="button" :disabled="pruning" @click="onPrune(false)">Prune anonymous</button>
      <button type="button" class="danger" :disabled="pruning" @click="onPrune(true)">Prune unused</button>
      <span v-if="pruneResult" class="muted" role="status">{{ pruneResult }}</span>
    </div>
    <p v-if="pruneError" class="error" role="alert">{{ pruneError }}</p>
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
