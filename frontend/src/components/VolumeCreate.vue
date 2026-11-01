<script setup lang="ts">
import { computed, ref } from 'vue';

import { createVolume, type Volume } from '../api/client';
import { parsePairs } from '../format';
import { currentUser } from '../session';

const props = defineProps<{ env: string }>();
const emit = defineEmits<{ created: [volume: Volume] }>();

// A user's volumes are local, without driver options, and named (the server prefixes the
// name with theirs).
const isAdmin = computed(() => !!currentUser.value?.admin);
const name = ref('');
const driver = ref('');
const options = ref('');
const labels = ref('');
const busy = ref(false);
const error = ref('');
const result = ref('');

async function onSubmit() {
  busy.value = true;
  error.value = '';
  result.value = '';
  try {
    const v = await createVolume(props.env, {
      name: name.value.trim() || undefined,
      driver: driver.value.trim() || undefined,
      options: parsePairs(options.value),
      labels: parsePairs(labels.value),
    });
    result.value = `Created volume ${v.name}.`;
    name.value = driver.value = options.value = labels.value = '';
    emit('created', v);
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
        <input
          v-model="name"
          name="name"
          maxlength="255"
          :placeholder="isAdmin ? 'generated if empty' : 'data'"
          :required="!isAdmin"
          :disabled="busy"
        />
      </label>
      <label v-if="isAdmin">
        Driver
        <input v-model="driver" name="driver" maxlength="255" placeholder="local" :disabled="busy" />
      </label>
    </div>
    <div class="row">
      <label v-if="isAdmin">
        Driver options (one key=value per line)
        <textarea
          v-model="options"
          name="options"
          rows="3"
          placeholder="type=nfs&#10;o=addr=10.0.0.5,rw&#10;device=:/export"
          :disabled="busy"
        ></textarea>
      </label>
      <label>
        Labels (one key=value per line)
        <textarea v-model="labels" name="labels" rows="3" :disabled="busy"></textarea>
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

label {
  display: flex;
  flex: 1 1 14rem;
  flex-direction: column;
  gap: 0.25rem;
  font-weight: 500;
}

.error {
  margin: 0.5rem 0 0;
}
</style>
