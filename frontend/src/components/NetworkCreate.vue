<script setup lang="ts">
import { computed, reactive, ref } from 'vue';

import { createNetwork } from '../api/client';
import { parsePairs } from '../format';
import { currentUser } from '../session';

const props = defineProps<{ env: string }>();
const emit = defineEmits<{ created: [id: string] }>();

// A user's networks are bridges Docker picks the addresses of, without driver options,
// IPv6 or attaching.
const isAdmin = computed(() => !!currentUser.value?.admin);
const empty = () => ({
  name: '',
  driver: '',
  subnet: '',
  gateway: '',
  ip_range: '',
  internal: false,
  attachable: false,
  ipv6: false,
  options: '',
  labels: '',
});
const form = reactive(empty());
const busy = ref(false);
const error = ref('');
const result = ref('');

async function onSubmit() {
  busy.value = true;
  error.value = '';
  result.value = '';
  try {
    const name = form.name.trim();
    const r = await createNetwork(props.env, {
      name,
      driver: form.driver.trim() || undefined,
      subnet: form.subnet.trim() || undefined,
      gateway: form.gateway.trim() || undefined,
      ip_range: form.ip_range.trim() || undefined,
      internal: form.internal,
      attachable: form.attachable,
      ipv6: form.ipv6,
      options: parsePairs(form.options),
      labels: parsePairs(form.labels),
    });
    result.value = [`Created network ${name}.`, ...r.warnings].join(' ');
    Object.assign(form, empty());
    emit('created', r.id);
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e);
  } finally {
    busy.value = false;
  }
}
</script>

<template>
  <form class="create" @submit.prevent="onSubmit">
    <div class="row">
      <label>
        Name
        <input v-model="form.name" name="name" maxlength="255" required :disabled="busy" />
      </label>
      <label v-if="isAdmin">
        Driver
        <input v-model="form.driver" name="driver" maxlength="255" placeholder="bridge" :disabled="busy" />
      </label>
    </div>
    <div v-if="isAdmin" class="row">
      <label>
        Subnet
        <input
          v-model="form.subnet"
          name="subnet"
          placeholder="automatic, or 172.30.0.0/16"
          :disabled="busy"
        />
      </label>
      <label>
        Gateway
        <input v-model="form.gateway" name="gateway" placeholder="172.30.0.1" :disabled="busy" />
      </label>
      <label>
        IP range
        <input v-model="form.ip_range" name="ip_range" placeholder="172.30.5.0/24" :disabled="busy" />
      </label>
    </div>
    <div class="checks">
      <label>
        <input v-model="form.internal" type="checkbox" :disabled="busy" />
        Internal (no outside access)
      </label>
      <template v-if="isAdmin">
        <label><input v-model="form.attachable" type="checkbox" :disabled="busy" /> Attachable</label>
        <label><input v-model="form.ipv6" type="checkbox" :disabled="busy" /> IPv6</label>
      </template>
    </div>
    <div class="row">
      <label v-if="isAdmin">
        Driver options (one key=value per line)
        <textarea v-model="form.options" name="options" rows="3" :disabled="busy"></textarea>
      </label>
      <label>
        Labels (one key=value per line)
        <textarea v-model="form.labels" name="labels" rows="3" :disabled="busy"></textarea>
      </label>
    </div>
    <div>
      <button type="submit" class="primary" :disabled="busy">{{ busy ? 'Creating…' : 'Create' }}</button>
    </div>
  </form>
  <p v-if="error" class="error" role="alert">{{ error }}</p>
  <p v-if="result" class="muted" role="status">{{ result }}</p>
</template>

<style scoped>
.create {
  display: flex;
  flex-direction: column;
  gap: 0.75rem;
}

.row {
  display: flex;
  flex-wrap: wrap;
  gap: 0.75rem;
}

.row label {
  display: flex;
  flex: 1 1 12rem;
  flex-direction: column;
  gap: 0.25rem;
  font-weight: 500;
}

.checks {
  display: flex;
  flex-wrap: wrap;
  gap: 0.5rem 1.25rem;
}

.checks label {
  display: inline-flex;
  align-items: center;
  gap: 0.3rem;
}

.error {
  margin: 0.5rem 0 0;
}
</style>
