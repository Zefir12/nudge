<script setup lang="ts">
import { RouterLink } from 'vue-router';

import { getEnvironment, listNetworks, listVolumes } from '../api/client';
import KeyValues from '../components/KeyValues.vue';
import LoadState from '../components/LoadState.vue';
import { useLoad } from '../composables/useLoad';
import { useParams } from '../composables/params';
import { formatBytes } from '../format';

const { env } = useParams();

// Docker's info has container and image counts; volumes and networks need their lists.
const { data, error, reload } = useLoad(env, async (id) => {
  const [detail, volumes, networks] = await Promise.all([
    getEnvironment(id),
    listVolumes(id),
    listNetworks(id),
  ]);
  return { detail, volumes: volumes.length, networks: networks.length };
});
</script>

<template>
  <h1>{{ data?.detail.name ?? 'Dashboard' }}</h1>
  <LoadState :loading="!data" :error="error" :ready="data !== null" @retry="reload">
    <template v-if="data">
      <section class="counts">
        <RouterLink class="card count" :to="{ name: 'containers', params: { env } }">
          <span class="number">{{ data.detail.docker.containers }}</span>
          <span>containers</span>
          <span class="muted">
            {{ data.detail.docker.containers_running }} running ·
            {{ data.detail.docker.containers_stopped }} stopped
            <template v-if="data.detail.docker.containers_paused">
              · {{ data.detail.docker.containers_paused }} paused
            </template>
          </span>
        </RouterLink>
        <RouterLink class="card count" :to="{ name: 'images', params: { env } }">
          <span class="number">{{ data.detail.docker.images }}</span>
          <span>images</span>
        </RouterLink>
        <RouterLink class="card count" :to="{ name: 'volumes', params: { env } }">
          <span class="number">{{ data.volumes }}</span>
          <span>volumes</span>
        </RouterLink>
        <RouterLink class="card count" :to="{ name: 'networks', params: { env } }">
          <span class="number">{{ data.networks }}</span>
          <span>networks</span>
        </RouterLink>
      </section>
      <section class="card">
        <h2>Host</h2>
        <KeyValues
          :rows="[
            ['Host name', data.detail.docker.name],
            ['Docker', data.detail.docker.version],
            ['API version', `${data.detail.docker.api_version} (min ${data.detail.docker.min_api_version})`],
            ['Operating system', data.detail.docker.os],
            ['Kernel', data.detail.docker.kernel_version],
            ['Architecture', data.detail.docker.architecture],
            ['CPUs', data.detail.docker.ncpu],
            ['Memory', formatBytes(data.detail.docker.mem_total)],
            ['Storage driver', data.detail.docker.storage_driver],
            ['Docker root', data.detail.docker.docker_root_dir],
            ['Kind', data.detail.kind],
          ]"
        />
      </section>
    </template>
  </LoadState>
</template>

<style scoped>
.counts {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(11rem, 1fr));
  gap: 1rem;
  margin-bottom: 1rem;
}

.count {
  display: flex;
  flex-direction: column;
  color: var(--text);
  text-decoration: none;
}

.count:hover {
  border-color: var(--accent);
}

.number {
  font-size: 1.75rem;
  font-weight: 700;
}
</style>
