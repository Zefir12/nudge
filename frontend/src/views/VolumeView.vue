<script setup lang="ts">
import { computed, ref } from 'vue';
import { RouterLink, useRouter } from 'vue-router';

import { getVolume, removeVolume } from '../api/client';
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

const router = useRouter();
const removing = ref(false);
const removeError = ref('');
const force = ref(false);

async function onRemove() {
  if (!window.confirm(`Remove the volume "${id.value}"? Its data is lost.`)) return;
  removing.value = true;
  removeError.value = '';
  try {
    await removeVolume(env.value, id.value, force.value);
    await router.push({ name: 'volumes', params: { env: env.value } });
  } catch (e) {
    removeError.value = e instanceof Error ? e.message : String(e);
  } finally {
    removing.value = false;
  }
}

const labels = computed(() => Object.entries(v.value?.labels ?? {}).sort(([a], [b]) => a.localeCompare(b)));
</script>

<template>
  <p><RouterLink :to="{ name: 'volumes', params: { env } }">← Volumes</RouterLink></p>
  <LoadState :loading="!v" :error="error" :ready="v !== null" @retry="reload">
    <template v-if="v">
      <h1 class="break">{{ v.name }}</h1>
      <div class="actions">
        <button type="button" class="danger" :disabled="removing" @click="onRemove">Remove</button>
        <label>
          <input v-model="force" type="checkbox" :disabled="removing" />
          force (even if the driver can't remove it)
        </label>
      </div>
      <p v-if="removeError" class="error" role="alert">{{ removeError }}</p>
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
.actions {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 0.5rem;
  margin-bottom: 1rem;
}

.actions label {
  display: inline-flex;
  align-items: center;
  gap: 0.3rem;
}

.card + .card {
  margin-top: 1rem;
}
</style>
