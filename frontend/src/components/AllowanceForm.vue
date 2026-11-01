<script setup lang="ts">
import { ref, watch } from 'vue';

import {
  deleteAllowance,
  getAllowance,
  setAllowance,
  type Account,
  type Allowance,
  type Environment,
} from '../api/client';
import AllowanceUsage from './AllowanceUsage.vue';

const props = defineProps<{ user: Account; environments: Environment[] }>();
const emit = defineEmits<{ close: [] }>();

const mib = 1024 ** 2;
const gib = 1024 ** 3;

const env = ref<number | null>(props.environments[0]?.id ?? null);
const current = ref<Allowance | null>(null);
const error = ref('');
const busy = ref(false);

// The form edits CPUs, memory in MiB and disk in GiB; the API takes thousandths and bytes.
const emptyForm = () => ({
  cpus: 1,
  memory: 1024,
  disk: 10,
  containers: 5,
  portMin: 0,
  portMax: 0,
  images: '',
});
const form = ref(emptyForm());

const message = (e: unknown) => (e instanceof Error ? e.message : String(e));

async function load() {
  current.value = null;
  error.value = '';
  if (env.value === null) return;
  try {
    const a = await getAllowance(String(env.value), props.user.id);
    current.value = a;
    const l = a.limits;
    form.value = l
      ? {
          cpus: l.cpus_milli / 1000,
          memory: Math.round(l.memory_bytes / mib),
          disk: Math.round((l.disk_bytes / gib) * 10) / 10,
          containers: l.max_containers,
          portMin: l.port_min,
          portMax: l.port_max,
          images: l.images.join('\n'),
        }
      : emptyForm();
  } catch (e) {
    error.value = message(e);
  }
}

watch(env, load, { immediate: true });

async function onSave() {
  if (env.value === null) return;
  busy.value = true;
  error.value = '';
  const f = form.value;
  try {
    await setAllowance(String(env.value), props.user.id, {
      cpus_milli: Math.round(f.cpus * 1000),
      memory_bytes: Math.round(f.memory) * mib,
      disk_bytes: Math.round(f.disk * gib),
      max_containers: f.containers,
      port_min: f.portMin,
      port_max: f.portMax,
      images: f.images.split(/\s+/).filter((i) => i !== ''),
    });
    await load();
  } catch (e) {
    error.value = message(e);
  } finally {
    busy.value = false;
  }
}

async function onRemove() {
  if (env.value === null) return;
  if (
    !window.confirm(
      `Remove ${props.user.username}'s allowance? They can't create or start containers there then.`,
    )
  ) {
    return;
  }
  busy.value = true;
  error.value = '';
  try {
    await deleteAllowance(String(env.value), props.user.id);
    await load();
  } catch (e) {
    error.value = message(e);
  } finally {
    busy.value = false;
  }
}
</script>

<template>
  <form class="allowance" @submit.prevent="onSave">
    <fieldset>
      <legend>Allowance of {{ user.username }}</legend>
      <p v-if="environments.length === 0" class="muted">There are no environments yet.</p>
      <label>
        Environment
        <select v-model="env" name="environment">
          <option v-for="e in environments" :key="e.id" :value="e.id">{{ e.name }}</option>
        </select>
      </label>
      <label>
        CPUs
        <input v-model.number="form.cpus" name="cpus" type="number" min="0" step="0.001" required />
      </label>
      <label>
        Memory (MiB)
        <input v-model.number="form.memory" name="memory" type="number" min="0" step="1" required />
      </label>
      <label>
        Disk (GiB)
        <input v-model.number="form.disk" name="disk" type="number" min="0" step="0.1" required />
      </label>
      <label>
        Containers
        <input v-model.number="form.containers" name="containers" type="number" min="0" step="1" required />
      </label>
      <label>
        Host ports from
        <input v-model.number="form.portMin" name="port_min" type="number" min="0" max="65535" required />
      </label>
      <label>
        to
        <input v-model.number="form.portMax" name="port_max" type="number" min="0" max="65535" required />
      </label>
      <label class="images">
        Images (one pattern per line, empty for any)
        <textarea
          v-model="form.images"
          name="images"
          rows="2"
          placeholder="nginx&#10;ghcr.io/me/*"
        ></textarea>
      </label>
    </fieldset>
    <p class="muted hint">
      Every container of theirs counts, running or not. Host ports 0 and 0 allow none. Without an allowance
      they can't create or start containers in the environment.
    </p>
    <AllowanceUsage v-if="current?.limits" :allowance="current" />
    <p v-if="error" class="error" role="alert">{{ error }}</p>
    <div class="buttons">
      <button type="submit" class="primary" :disabled="busy || env === null">Save</button>
      <button v-if="current?.limits" type="button" class="danger" :disabled="busy" @click="onRemove">
        Remove allowance
      </button>
      <button type="button" @click="emit('close')">Close</button>
    </div>
  </form>
</template>

<style scoped>
.allowance {
  display: flex;
  flex-direction: column;
  gap: 0.75rem;
  margin-top: 1rem;
}

fieldset {
  display: flex;
  flex-wrap: wrap;
  align-items: flex-end;
  gap: 0.75rem;
  border: 0;
  margin: 0;
  padding: 0;
}

legend {
  font-weight: 500;
  margin-bottom: 0.25rem;
}

label {
  display: flex;
  flex-direction: column;
  gap: 0.25rem;
  font-weight: 500;
}

input[type='number'] {
  width: 8rem;
}

.images {
  flex-basis: 100%;
}

.hint {
  margin: 0;
}

.buttons {
  display: flex;
  gap: 0.5rem;
}
</style>
