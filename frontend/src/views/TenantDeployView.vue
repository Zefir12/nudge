<script setup lang="ts">
import { computed, ref } from 'vue';
import { RouterLink, useRouter } from 'vue-router';

import {
  ApiError,
  createTenantContainer,
  createVolume,
  getAllowance,
  getTenantSpec,
  listNetworks,
  listVolumes,
  recreateTenantContainer,
  type TenantSpec,
} from '../api/client';
import LoadState from '../components/LoadState.vue';
import TenantForm from '../components/TenantForm.vue';
import { useLoad } from '../composables/useLoad';
import { useParams } from '../composables/params';

// A user deploys a container from the tenant form (/deploy), or changes one, which
// recreates it through the server's builder (/containers/:id/redeploy). The form's limits
// come from the user's allowance in the environment.
const { env, id } = useParams();
const editing = computed(() => id.value !== '');
const {
  data,
  error: loadError,
  reload,
} = useLoad(
  // A new container has no ID, which useLoad would take for a page being left.
  (): readonly [string, string?] => (editing.value ? [env.value, id.value] : [env.value]),
  async ([e, i]) => {
    const [allowance, volumes, networks, spec] = await Promise.all([
      getAllowance(e),
      listVolumes(e),
      listNetworks(e),
      i ? getTenantSpec(e, i) : Promise.resolve(undefined),
    ]);
    return { allowance, volumes, networks, spec };
  },
);

const router = useRouter();
const start = ref(true);
const busy = ref(false);
const error = ref('');
const fields = ref<Record<string, string>>({});
// created maps the new volumes this page made to their full names, so a retry after a
// failed deploy uses them instead of creating them again.
const created = new Map<string, string>();

async function onSubmit(s: TenantSpec, create: number[]) {
  if (
    editing.value &&
    !window.confirm(`Recreate "${data.value?.spec?.name}" with these settings? It is stopped and replaced.`)
  )
    return;
  busy.value = true;
  error.value = '';
  fields.value = {};
  try {
    // New volumes first: the spec names them by their full (prefixed) name.
    for (const i of create) {
      const m = s.mounts[i];
      if (!m) continue;
      const key = `mounts.${i}.volume`;
      if (!m.volume) throw new ApiError(400, 'invalid container settings', { [key]: 'name the new volume' });
      const done = created.get(m.volume);
      if (done) {
        m.volume = done;
        continue;
      }
      try {
        const v = await createVolume(env.value, { name: m.volume });
        created.set(m.volume, v.name);
        m.volume = v.name;
      } catch (e) {
        if (e instanceof ApiError) throw new ApiError(e.status, e.message, { ...e.fields, [key]: e.message });
        throw e;
      }
    }
    const res = editing.value
      ? await recreateTenantContainer(env.value, id.value, s)
      : await createTenantContainer(env.value, s, start.value);
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
      ← {{ data?.spec?.name ?? 'Container' }}
    </RouterLink>
  </p>
  <p v-else><RouterLink :to="{ name: 'containers', params: { env } }">← Containers</RouterLink></p>
  <h1 class="break">{{ editing ? `Edit ${data?.spec?.name ?? ''}` : 'Deploy a container' }}</h1>
  <p class="muted">
    {{
      editing
        ? 'Saving pulls the image and recreates the container: it is stopped and replaced by a new one with these settings.'
        : 'The image is pulled and the container started with these settings, within your allowance.'
    }}
  </p>
  <section class="card">
    <LoadState :loading="!data" :error="loadError" :ready="data !== null" @retry="reload">
      <template v-if="data">
        <p v-if="!data.allowance.limits || !data.allowance.left" class="muted none">
          You have no allowance in this environment: ask an administrator for one.
        </p>
        <TenantForm
          v-else
          :key="id || 'new'"
          :initial="data.spec"
          :limits="data.allowance.limits"
          :left="data.allowance.left"
          :volumes="data.volumes"
          :networks="data.networks"
          :fields="fields"
          :busy="busy"
          :submit-label="editing ? (busy ? 'Recreating…' : 'Recreate') : busy ? 'Deploying…' : 'Deploy'"
          @submit="onSubmit"
        >
          <label v-if="!editing" class="check"
            ><input v-model="start" type="checkbox" :disabled="busy" /> Start it</label
          >
        </TenantForm>
      </template>
    </LoadState>
    <p v-if="error" class="error" role="alert">{{ error }}</p>
  </section>
</template>

<style scoped>
.check {
  display: inline-flex;
  align-items: center;
  gap: 0.3rem;
}

.none {
  margin: 0;
}

.error {
  margin: 0.75rem 0 0;
}
</style>
