<script setup lang="ts">
import { computed, ref } from 'vue';
import { RouterLink, useRouter } from 'vue-router';

import {
  ApiError,
  createContainer,
  getContainerSpec,
  recreateContainer,
  type ContainerSpec,
} from '../api/client';
import ContainerForm from '../components/ContainerForm.vue';
import LoadState from '../components/LoadState.vue';
import { useLoad } from '../composables/useLoad';
import { useParams } from '../composables/params';

// One view for a new container (/containers/new) and for editing one, which recreates it
// with the changed settings (/containers/:id/edit).
const { env, id } = useParams();
const editing = computed(() => id.value !== '');
const {
  data: spec,
  error: loadError,
  reload,
} = useLoad(
  () => (editing.value ? ([env.value, id.value] as const) : null),
  (s) => (s ? getContainerSpec(s[0], s[1]) : Promise.resolve(null)),
);

const router = useRouter();
const start = ref(true);
const pull = ref(false);
const busy = ref(false);
const error = ref('');
const fields = ref<Record<string, string>>({});

async function onSubmit(s: ContainerSpec) {
  if (
    editing.value &&
    !window.confirm(`Recreate "${spec.value?.name}" with these settings? It is stopped and replaced.`)
  )
    return;
  busy.value = true;
  error.value = '';
  fields.value = {};
  try {
    const res = editing.value
      ? await recreateContainer(env.value, id.value, { pull: pull.value, spec: s })
      : await createContainer(env.value, s, start.value);
    await router.push({ name: 'container', params: { env: env.value, id: res.id } });
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e);
    if (e instanceof ApiError) fields.value = e.fields;
  } finally {
    busy.value = false;
  }
}
</script>

<template>
  <p v-if="editing">
    <RouterLink :to="{ name: 'container', params: { env, id } }">
      ← {{ spec?.name ?? 'Container' }}
    </RouterLink>
  </p>
  <p v-else><RouterLink :to="{ name: 'containers', params: { env } }">← Containers</RouterLink></p>
  <h1 class="break">{{ editing ? `Edit ${spec?.name ?? ''}` : 'New container' }}</h1>
  <p v-if="editing" class="muted">
    Saving recreates the container: it is stopped and replaced by a new one with these settings. Settings not
    shown here (such as capabilities or devices) are kept.
  </p>
  <section class="card">
    <LoadState v-if="editing" :loading="!spec" :error="loadError" :ready="spec !== null" @retry="reload">
      <ContainerForm
        v-if="spec"
        :initial="spec"
        :fields="fields"
        :busy="busy"
        :submit-label="busy ? 'Recreating…' : 'Recreate'"
        @submit="onSubmit"
      >
        <label class="check">
          <input v-model="pull" type="checkbox" :disabled="busy" />
          Pull the image first
        </label>
      </ContainerForm>
    </LoadState>
    <ContainerForm
      v-else
      :fields="fields"
      :busy="busy"
      :submit-label="busy ? 'Creating…' : 'Create'"
      @submit="onSubmit"
    >
      <label class="check"><input v-model="start" type="checkbox" :disabled="busy" /> Start it</label>
    </ContainerForm>
    <p v-if="error" class="error" role="alert">{{ error }}</p>
  </section>
</template>

<style scoped>
.check {
  display: inline-flex;
  align-items: center;
  gap: 0.3rem;
}

.error {
  margin: 0.75rem 0 0;
}
</style>
