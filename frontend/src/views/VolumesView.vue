<script setup lang="ts">
import { computed, ref } from 'vue';
import { RouterLink } from 'vue-router';

import { listVolumes, measureVolumes, pruneVolumes, type Volume } from '../api/client';
import LoadState from '../components/LoadState.vue';
import VolumeCreate from '../components/VolumeCreate.vue';
import { useLiveReload } from '../composables/useEvents';
import { useLoad } from '../composables/useLoad';
import { useParams } from '../composables/params';
import { formatBytes, formatDate, matches } from '../format';

const { env } = useParams();
const { data, error, reload } = useLoad(env, listVolumes);
// Docker events refresh the list, so changes made elsewhere show up by themselves.
useLiveReload(env, ['volume'], reload);

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

const measuring = ref(false);

async function onMeasure() {
  measuring.value = true;
  pruneError.value = '';
  try {
    await measureVolumes(env.value);
    await reload();
  } catch (e) {
    pruneError.value = e instanceof Error ? e.message : String(e);
  } finally {
    measuring.value = false;
  }
}

// bySize sorts the largest first; the API lists by name.
const bySize = ref(false);

const shown = computed(() => {
  const vols = (data.value ?? []).filter((v) => matches(query.value, v.name, v.driver));
  return bySize.value ? [...vols].sort((a, b) => (b.size ?? -1) - (a.size ?? -1)) : vols;
});

const sizeText = (v: Volume) => (v.size === null ? '—' : formatBytes(v.size));

function sizeTitle(v: Volume): string {
  if (!v.size_measured_at) return 'Not measured yet';
  const at = `Measured ${formatDate(v.size_measured_at)}`;
  return v.size === null ? `${at}; the driver can't report its size` : at;
}
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
      <button type="button" :disabled="measuring" @click="onMeasure">
        {{ measuring ? 'Measuring…' : 'Measure sizes' }}
      </button>
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
              <th :aria-sort="bySize ? 'descending' : 'none'">
                <button type="button" class="sort" @click="bySize = !bySize">Size</button>
              </th>
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
              <td class="size" :title="sizeTitle(v)">{{ sizeText(v) }}</td>
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

.sort {
  padding: 0;
  border: none;
  background: none;
  font: inherit;
  color: inherit;
  cursor: pointer;
}

.size {
  white-space: nowrap;
}

.search {
  width: 100%;
  margin-bottom: 0.5rem;
}
</style>
