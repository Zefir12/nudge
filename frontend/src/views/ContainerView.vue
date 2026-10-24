<script setup lang="ts">
import { computed, defineAsyncComponent, ref } from 'vue';
import { RouterLink, useRouter } from 'vue-router';

import { getContainer, recreateContainer, updateContainer } from '../api/client';
import ContainerActions from '../components/ContainerActions.vue';
import ContainerLogs from '../components/ContainerLogs.vue';
import ContainerStats from '../components/ContainerStats.vue';
import KeyValues from '../components/KeyValues.vue';
import LoadState from '../components/LoadState.vue';
import StateBadge from '../components/StateBadge.vue';
import UpdateBadge from '../components/UpdateBadge.vue';
import UpdateHistory from '../components/UpdateHistory.vue';
import { useLoad } from '../composables/useLoad';
import { useParams } from '../composables/params';
import { useUpdates } from '../composables/useUpdates';
import { formatDate, formatPort, shortId } from '../format';

// The terminal (xterm.js) loads on the page of a running container only.
const ContainerTerminal = defineAsyncComponent(() => import('../components/ContainerTerminal.vue'));

const { env, id } = useParams();
const router = useRouter();
const {
  data: c,
  error,
  reload,
} = useLoad(
  () => [env.value, id.value] as const,
  ([e, i]) => getContainer(e, i),
);

const pull = ref(false);
const recreating = ref(false);
const recreateError = ref('');

async function onRecreate() {
  if (!c.value) return;
  const how = pull.value ? 'Pull its image and recreate' : 'Recreate';
  if (!window.confirm(`${how} "${c.value.name}" with the same settings? It is stopped and replaced.`)) return;
  recreating.value = true;
  recreateError.value = '';
  try {
    const res = await recreateContainer(env.value, c.value.id, { pull: pull.value });
    await router.replace({ name: 'container', params: { env: env.value, id: res.id } });
  } catch (e) {
    recreateError.value = e instanceof Error ? e.message : String(e);
  } finally {
    recreating.value = false;
  }
}

// An update pulls the image and recreates the container only if that brought a newer one.
const { updateFor, reload: reloadUpdates } = useUpdates(env);
const update = computed(() => (c.value ? updateFor(c.value) : undefined));
const updateNote = ref('');

async function onUpdate() {
  if (!c.value) return;
  if (!window.confirm(`Pull the newest ${c.value.image} and recreate "${c.value.name}" with it?`)) return;
  recreating.value = true;
  recreateError.value = '';
  updateNote.value = '';
  try {
    const res = await updateContainer(env.value, c.value.id);
    if (res.updated) {
      await router.replace({ name: 'container', params: { env: env.value, id: res.id } });
    } else {
      updateNote.value = 'The registry has nothing newer: the container was left as it is.';
    }
    void reloadUpdates();
  } catch (e) {
    recreateError.value = e instanceof Error ? e.message : String(e);
  } finally {
    recreating.value = false;
  }
}

const onRemoved = () => router.push({ name: 'containers', params: { env: env.value } });

const command = computed(() => (c.value ? [c.value.path, ...c.value.args].join(' ') : ''));
const labels = computed(() =>
  Object.entries(c.value?.config.labels ?? {}).sort(([a], [b]) => a.localeCompare(b)),
);
</script>

<template>
  <p><RouterLink :to="{ name: 'containers', params: { env } }">← Containers</RouterLink></p>
  <LoadState :loading="!c" :error="error" :ready="c !== null" @retry="reload">
    <template v-if="c">
      <h1>
        {{ c.name }} <StateBadge :state="c.state.status" :health="c.state.health" /><UpdateBadge
          v-if="update"
          :update="update"
        />
      </h1>
      <ContainerActions
        class="detail-actions"
        :env="env"
        :id="c.id"
        :name="c.name"
        :state="c.state.status"
        volumes-option
        @changed="reload"
        @removed="onRemoved"
      />
      <div class="recreate">
        <RouterLink class="button" :to="{ name: 'container-edit', params: { env, id: c.id } }"
          >Edit</RouterLink
        >
        <button v-if="update" type="button" class="primary" :disabled="recreating" @click="onUpdate">
          Update
        </button>
        <button type="button" :disabled="recreating" @click="onRecreate">
          {{ recreating ? 'Recreating…' : 'Recreate' }}
        </button>
        <label><input v-model="pull" type="checkbox" :disabled="recreating" /> pull the image first</label>
      </div>
      <p v-if="recreateError" class="error" role="alert">{{ recreateError }}</p>
      <p v-if="updateNote" class="muted" role="status">{{ updateNote }}</p>
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

      <UpdateHistory :env="env" kind="container" :target="c.name" />

      <ContainerStats v-if="c.state.running" :env="env" :id="c.id" />

      <ContainerLogs :env="env" :id="c.id" :name="c.name" />

      <ContainerTerminal v-if="c.state.running" :env="env" :id="c.id" />

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
.detail-actions {
  margin-bottom: 0.5rem;
}

.recreate {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 0.5rem;
  margin-bottom: 1rem;
}

.recreate label {
  display: inline-flex;
  align-items: center;
  gap: 0.3rem;
}

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
