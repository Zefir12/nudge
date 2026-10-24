<script setup lang="ts">
import { computed, ref } from 'vue';
import { RouterLink } from 'vue-router';

import { listContainers } from '../api/client';
import ContainerActions from '../components/ContainerActions.vue';
import LoadState from '../components/LoadState.vue';
import StateBadge from '../components/StateBadge.vue';
import { useLoad } from '../composables/useLoad';
import { useParams } from '../composables/params';
import { formatPort, matches } from '../format';

const { env } = useParams();
const { data, error, reload } = useLoad(env, listContainers);

const query = ref('');
// "stopped" groups the states of containers that aren't running.
const state = ref<'all' | 'running' | 'stopped'>('all');

const shown = computed(() =>
  (data.value ?? []).filter(
    (c) =>
      (state.value === 'all' ||
        (state.value === 'running' ? c.state === 'running' : c.state !== 'running')) &&
      matches(query.value, c.name, c.image, c.id),
  ),
);
</script>

<template>
  <div class="title">
    <h1>Containers</h1>
    <RouterLink class="button primary" :to="{ name: 'container-new', params: { env } }"
      >New container</RouterLink
    >
  </div>
  <section class="card">
    <div class="filters">
      <input v-model="query" type="search" placeholder="Search name, image or ID" aria-label="Search" />
      <select v-model="state" aria-label="State">
        <option value="all">All</option>
        <option value="running">Running</option>
        <option value="stopped">Stopped</option>
      </select>
    </div>
    <LoadState
      :loading="!data"
      :error="error"
      :ready="data !== null"
      :empty="shown.length === 0"
      :empty-text="data?.length ? 'No container matches.' : 'No containers.'"
      @retry="reload"
    >
      <div class="table-wrap">
        <table>
          <thead>
            <tr>
              <th>Name</th>
              <th>State</th>
              <th>Image</th>
              <th class="wide-only">Status</th>
              <th class="wide-only">Ports</th>
              <th>Actions</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="c in shown" :key="c.id">
              <td>
                <RouterLink :to="{ name: 'container', params: { env, id: c.id } }">{{ c.name }}</RouterLink>
              </td>
              <td><StateBadge :state="c.state" :health="c.health" /></td>
              <td class="break">{{ c.image }}</td>
              <td class="wide-only">{{ c.status }}</td>
              <td class="wide-only">
                <div v-for="p in c.ports" :key="formatPort(p)">
                  <code>{{ formatPort(p) }}</code>
                </div>
              </td>
              <td>
                <ContainerActions
                  :env="env"
                  :id="c.id"
                  :name="c.name"
                  :state="c.state"
                  @changed="reload"
                  @removed="reload"
                />
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </LoadState>
  </section>
</template>

<style scoped>
.title {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  justify-content: space-between;
  gap: 0.5rem;
}

.filters {
  display: flex;
  flex-wrap: wrap;
  gap: 0.5rem;
  margin-bottom: 0.5rem;
}

.filters input {
  flex: 1 1 14rem;
}
</style>
