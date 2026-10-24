<script setup lang="ts">
import { computed, onBeforeUnmount, ref } from 'vue';

import { pullImage, type PullEvent } from '../api/client';
import { formatBytes } from '../format';

const props = defineProps<{ env: string }>();
const emit = defineEmits<{ pulled: [image: string] }>();

interface Layer {
  id: string;
  status: string;
  current?: number;
  total?: number;
}

const image = ref('');
const busy = ref(false);
const error = ref('');
const result = ref('');
// status is the latest message about the image as a whole; layers keep Docker's order.
const status = ref('');
const layers = ref<Layer[]>([]);
let abort: AbortController | null = null;

const pct = (l: Layer) => (l.total ? Math.min(100, Math.round(((l.current ?? 0) / l.total) * 100)) : null);
const progress = computed(() => layers.value.map((l) => ({ ...l, pct: pct(l) })));

function onEvent(e: PullEvent) {
  const text = e.status ?? '';
  // Layer lines have a short hex id; the first line's id is the tag being pulled.
  if (!e.id || !/^[0-9a-f]{12}$/.test(e.id)) {
    status.value = e.id ? `${text} ${e.id}` : text;
    return;
  }
  const layer = layers.value.find((l) => l.id === e.id);
  const next = { id: e.id, status: text, current: e.current, total: e.total };
  if (layer) Object.assign(layer, next);
  else layers.value.push(next);
}

async function onPull() {
  const name = image.value.trim();
  if (!name) return;
  busy.value = true;
  error.value = '';
  result.value = '';
  status.value = '';
  layers.value = [];
  abort = new AbortController();
  try {
    await pullImage(props.env, name, onEvent, abort.signal);
    result.value = `Pulled ${name}.`;
    image.value = '';
    emit('pulled', name);
  } catch (e) {
    if (!abort.signal.aborted) error.value = e instanceof Error ? e.message : String(e);
  } finally {
    busy.value = false;
    abort = null;
  }
}

function onCancel() {
  abort?.abort();
  status.value = 'Cancelled.';
}

onBeforeUnmount(() => abort?.abort());
</script>

<template>
  <form class="pull" @submit.prevent="onPull">
    <input
      v-model="image"
      name="image"
      placeholder="nginx:1.29 or ghcr.io/org/app:tag"
      aria-label="Image to pull"
      maxlength="255"
      required
      :disabled="busy"
    />
    <button type="submit" class="primary" :disabled="busy">{{ busy ? 'Pulling…' : 'Pull' }}</button>
    <button v-if="busy" type="button" @click="onCancel">Cancel</button>
  </form>
  <p v-if="error" class="error" role="alert">{{ error }}</p>
  <p v-if="result" class="muted" role="status">{{ result }}</p>
  <div v-if="!result && (busy || status || layers.length)" class="progress" aria-live="polite">
    <p v-if="status" class="muted">{{ status }}</p>
    <ul>
      <li v-for="l in progress" :key="l.id">
        <code>{{ l.id }}</code>
        <span>{{ l.status }}</span>
        <template v-if="l.pct !== null">
          <progress :value="l.pct" max="100" :aria-label="`Layer ${l.id}`"></progress>
          <span class="muted">{{ formatBytes(l.current ?? 0) }} / {{ formatBytes(l.total ?? 0) }}</span>
        </template>
      </li>
    </ul>
  </div>
</template>

<style scoped>
.pull {
  display: flex;
  flex-wrap: wrap;
  gap: 0.5rem;
}

.pull input {
  flex: 1 1 16rem;
}

.progress ul {
  margin: 0.5rem 0 0;
  padding: 0;
  list-style: none;
}

.progress li {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 0.25rem 0.75rem;
  padding: 0.15rem 0;
}

.progress progress {
  width: 8rem;
}

.error {
  margin: 0.5rem 0 0;
}
</style>
