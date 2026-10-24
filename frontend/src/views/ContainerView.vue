<script setup lang="ts">
import { computed } from 'vue';
import { RouterLink } from 'vue-router';

import { getContainer } from '../api/client';
import KeyValues from '../components/KeyValues.vue';
import LoadState from '../components/LoadState.vue';
import StateBadge from '../components/StateBadge.vue';
import { useLoad } from '../composables/useLoad';
import { useParams } from '../composables/params';
import { formatDate, formatPort, shortId } from '../format';

const { env, id } = useParams();
const {
  data: c,
  error,
  reload,
} = useLoad(
  () => [env.value, id.value] as const,
  ([e, i]) => getContainer(e, i),
);

const command = computed(() => (c.value ? [c.value.path, ...c.value.args].join(' ') : ''));
const labels = computed(() =>
  Object.entries(c.value?.config.labels ?? {}).sort(([a], [b]) => a.localeCompare(b)),
);
</script>

<template>
  <p><RouterLink :to="{ name: 'containers', params: { env } }">← Containers</RouterLink></p>
  <LoadState :loading="!c" :error="error" :ready="c !== null" @retry="reload">
    <template v-if="c">
      <h1>{{ c.name }} <StateBadge :state="c.state.status" :health="c.state.health" /></h1>
      <section class="card">
        <h2>Overview</h2>
        <KeyValues
          :rows="[
            ['ID', shortId(c.id)],
            ['Image', c.image],
            ['Image ID', shortId(c.image_id)],
            ['Created', formatDate(c.created_at)],
            ['Started', formatDate(c.state.started_at)],
            ['Finished', formatDate(c.state.finished_at)],
            ['Exit code', c.state.running ? null : c.state.exit_code],
            ['Error', c.state.error],
            ['OOM killed', c.state.oom_killed],
            ['Restart policy', c.restart_policy.name],
            ['Restarts', c.restart_count],
            ['Command', command],
            ['Working dir', c.config.working_dir],
            ['User', c.config.user],
            ['Hostname', c.config.hostname],
            ['Network mode', c.network_mode],
            ['Privileged', c.privileged],
          ]"
        />
        <p>
          <RouterLink :to="{ name: 'image', params: { env, id: c.image_id } }">Open the image</RouterLink>
        </p>
      </section>

      <section class="card">
        <h2>Ports</h2>
        <p v-if="c.ports.length === 0" class="muted">No ports.</p>
        <ul v-else>
          <li v-for="p in c.ports" :key="formatPort(p)">
            <code>{{ formatPort(p) }}</code>
          </li>
        </ul>
      </section>

      <section class="card">
        <h2>Mounts</h2>
        <p v-if="c.mounts.length === 0" class="muted">No mounts.</p>
        <div v-else class="table-wrap">
          <table>
            <thead>
              <tr>
                <th>Destination</th>
                <th>Source</th>
                <th>Type</th>
                <th>Mode</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="m in c.mounts" :key="m.destination">
                <td class="break">
                  <code>{{ m.destination }}</code>
                </td>
                <td class="break">
                  <RouterLink
                    v-if="m.type === 'volume'"
                    :to="{ name: 'volume', params: { env, id: m.name } }"
                  >
                    {{ m.name }}
                  </RouterLink>
                  <code v-else>{{ m.source }}</code>
                </td>
                <td>{{ m.type }}</td>
                <td>{{ m.rw ? 'rw' : 'ro' }}</td>
              </tr>
            </tbody>
          </table>
        </div>
      </section>

      <section class="card">
        <h2>Networks</h2>
        <p v-if="c.networks.length === 0" class="muted">No networks.</p>
        <div v-else class="table-wrap">
          <table>
            <thead>
              <tr>
                <th>Network</th>
                <th>IP address</th>
                <th class="wide-only">Gateway</th>
                <th class="wide-only">Aliases</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="n in c.networks" :key="n.name">
                <td>
                  <RouterLink :to="{ name: 'network', params: { env, id: n.network_id || n.name } }">
                    {{ n.name }}
                  </RouterLink>
                </td>
                <td>{{ n.ip_address || '—' }}</td>
                <td class="wide-only">{{ n.gateway || '—' }}</td>
                <td class="wide-only">{{ n.aliases.join(', ') || '—' }}</td>
              </tr>
            </tbody>
          </table>
        </div>
      </section>

      <section class="card">
        <h2>Environment</h2>
        <p v-if="c.config.env.length === 0" class="muted">No variables.</p>
        <ul v-else class="mono-list">
          <li v-for="v in c.config.env" :key="v">
            <code class="break">{{ v }}</code>
          </li>
        </ul>
      </section>

      <section class="card">
        <h2>Labels</h2>
        <p v-if="labels.length === 0" class="muted">No labels.</p>
        <KeyValues v-else :rows="labels" mono />
      </section>
    </template>
  </LoadState>
</template>

<style scoped>
.card + .card {
  margin-top: 1rem;
}

h1 {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 0.75rem;
  word-break: break-all;
}

ul {
  margin: 0;
  padding-left: 1.25rem;
}

.mono-list {
  list-style: none;
  padding: 0;
}
</style>
