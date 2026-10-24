<script setup lang="ts">
import { computed } from 'vue';
import { RouterLink } from 'vue-router';

import { getVolume } from '../api/client';
import KeyValues from '../components/KeyValues.vue';
import LoadState from '../components/LoadState.vue';
import { useLoad } from '../composables/useLoad';
import { useParams } from '../composables/params';
import { formatDate } from '../format';

const { env, id } = useParams();
const {
  data: v,
  error,
  reload,
} = useLoad(
  () => [env.value, id.value] as const,
  ([e, i]) => getVolume(e, i),
);

const labels = computed(() => Object.entries(v.value?.labels ?? {}).sort(([a], [b]) => a.localeCompare(b)));
</script>

<template>
  <p><RouterLink :to="{ name: 'volumes', params: { env } }">← Volumes</RouterLink></p>
  <LoadState :loading="!v" :error="error" :ready="v !== null" @retry="reload">
    <template v-if="v">
      <h1 class="break">{{ v.name }}</h1>
      <section class="card">
        <KeyValues
          :rows="[
            ['Driver', v.driver],
            ['Scope', v.scope],
            ['Mountpoint', v.mountpoint],
            ['Created', formatDate(v.created_at)],
          ]"
        />
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
</style>
