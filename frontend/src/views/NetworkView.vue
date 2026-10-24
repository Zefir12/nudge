<script setup lang="ts">
import { computed } from 'vue';
import { RouterLink } from 'vue-router';

import { getNetwork } from '../api/client';
import KeyValues from '../components/KeyValues.vue';
import LoadState from '../components/LoadState.vue';
import { useLoad } from '../composables/useLoad';
import { useParams } from '../composables/params';
import { formatDate, shortId } from '../format';

const { env, id } = useParams();
const {
  data: n,
  error,
  reload,
} = useLoad(
  () => [env.value, id.value] as const,
  ([e, i]) => getNetwork(e, i),
);

const sorted = (m: Record<string, string> | undefined) =>
  Object.entries(m ?? {}).sort(([a], [b]) => a.localeCompare(b));
const labels = computed(() => sorted(n.value?.labels));
const options = computed(() => sorted(n.value?.options));
</script>

<template>
  <p><RouterLink :to="{ name: 'networks', params: { env } }">← Networks</RouterLink></p>
  <LoadState :loading="!n" :error="error" :ready="n !== null" @retry="reload">
    <template v-if="n">
      <h1 class="break">{{ n.name }}</h1>
      <section class="card">
        <h2>Overview</h2>
        <KeyValues
          :rows="[
            ['ID', shortId(n.id)],
            ['Driver', n.driver],
            ['Scope', n.scope],
            ['Created', formatDate(n.created_at)],
            ['Internal', n.internal],
            ['Attachable', n.attachable],
            ['Ingress', n.ingress],
          ]"
        />
        <h2 class="sub">IP address management</h2>
        <p v-if="n.ipam.length === 0" class="muted">No subnets.</p>
        <KeyValues
          v-for="(c, i) in n.ipam"
          :key="i"
          :rows="[
            ['Subnet', c.subnet],
            ['IP range', c.ip_range],
            ['Gateway', c.gateway],
          ]"
        />
      </section>
      <section class="card">
        <h2>Containers</h2>
        <p v-if="n.containers.length === 0" class="muted">No containers.</p>
        <div v-else class="table-wrap">
          <table>
            <thead>
              <tr>
                <th>Container</th>
                <th>IPv4</th>
                <th class="wide-only">IPv6</th>
                <th class="wide-only">MAC</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="c in n.containers" :key="c.id">
                <td>
                  <RouterLink :to="{ name: 'container', params: { env, id: c.id } }">{{
                    c.name || shortId(c.id)
                  }}</RouterLink>
                </td>
                <td>{{ c.ipv4_address || '—' }}</td>
                <td class="wide-only">{{ c.ipv6_address || '—' }}</td>
                <td class="wide-only">{{ c.mac_address || '—' }}</td>
              </tr>
            </tbody>
          </table>
        </div>
      </section>
      <section class="card">
        <h2>Options</h2>
        <p v-if="options.length === 0" class="muted">No options.</p>
        <KeyValues v-else :rows="options" mono />
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

.sub {
  margin-top: 1rem;
}
</style>
