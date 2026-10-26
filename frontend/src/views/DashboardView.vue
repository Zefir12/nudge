<script setup lang="ts">
import { RouterLink } from 'vue-router';

import { getEnvironment, listNetworks, listVolumes } from '../api/client';
import EnvironmentUsage from '../components/EnvironmentUsage.vue';
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
  <div class="head">
    <span v-if="data" class="lamp" :class="{ on: data.detail.online }" aria-hidden="true" />
    <h1>{{ data?.detail.name ?? 'Dashboard' }}</h1>
    <span v-if="data" class="chip">{{ data.detail.kind === 'local' ? 'socket' : 'agent' }}</span>
  </div>
  <p v-if="data" class="host">
    Docker {{ data.detail.docker.version }} · {{ data.detail.docker.os_type }}/{{
      data.detail.docker.architecture
    }}
    · {{ data.detail.docker.ncpu }} CPU · {{ formatBytes(data.detail.docker.mem_total) }}
  </p>
  <LoadState :loading="!data" :error="error" :ready="data !== null" @retry="reload">
    <template v-if="data">
      <section class="counts">
        <RouterLink class="count" :to="{ name: 'containers', params: { env } }">
          <span class="label">Containers</span>
          <span class="number">{{ data.detail.docker.containers }}</span>
          <span class="muted">
            {{ data.detail.docker.containers_running }} running ·
            {{ data.detail.docker.containers_stopped }} stopped
            <template v-if="data.detail.docker.containers_paused">
              · {{ data.detail.docker.containers_paused }} paused
            </template>
          </span>
        </RouterLink>
        <RouterLink class="count" :to="{ name: 'images', params: { env } }">
          <span class="label">Images</span>
          <span class="number">{{ data.detail.docker.images }}</span>
        </RouterLink>
        <RouterLink class="count" :to="{ name: 'volumes', params: { env } }">
          <span class="label">Volumes</span>
          <span class="number">{{ data.volumes }}</span>
        </RouterLink>
        <RouterLink class="count" :to="{ name: 'networks', params: { env } }">
          <span class="label">Networks</span>
          <span class="number">{{ data.networks }}</span>
        </RouterLink>
      </section>
      <EnvironmentUsage :env="env" />
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
.head {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 0.75rem;
}

.head .lamp {
  width: 10px;
  height: 10px;
}

h1 {
  margin: 0;
}

.host {
  margin: 0.4rem 0 1.75rem;
  font-family: var(--mono);
  font-size: 12.5px;
  color: var(--muted);
}

.counts {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(11rem, 1fr));
  gap: 1rem;
  margin-bottom: 1rem;
}

.count {
  display: flex;
  flex-direction: column;
  gap: 0.2rem;
  padding: 1.1rem 1.25rem;
  color: var(--text);
  text-decoration: none;
  background: var(--surface);
  border: 1px solid var(--border);
  border-radius: var(--radius-lg);
  transition: border-color 0.15s ease;
}

.count:hover {
  color: var(--text);
  border-color: var(--accent-border);
}

.count .muted {
  font-size: 12.5px;
}

.label {
  font-size: 11px;
  font-weight: 600;
  color: var(--muted);
  text-transform: uppercase;
  letter-spacing: 0.14em;
}

.card + .card {
  margin-top: 1rem;
}

.number {
  font-family: var(--font-display);
  font-size: 2.1rem;
  font-weight: 600;
  line-height: 1.1;
}
</style>
