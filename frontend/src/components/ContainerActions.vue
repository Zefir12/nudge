<script setup lang="ts">
import { computed, ref } from 'vue';

import { containerAction, removeContainer, type ContainerAction } from '../api/client';

const props = defineProps<{
  env: string;
  id: string;
  name: string;
  state: string;
  // volumesOption shows a checkbox to remove the container's anonymous volumes too.
  volumesOption?: boolean;
}>();
const emit = defineEmits<{ changed: []; removed: [] }>();

const busy = ref(false);
const error = ref('');
const volumes = ref(false);

// A paused or restarting container has a process to stop, like a running one.
const up = computed(() => ['running', 'paused', 'restarting'].includes(props.state));

async function run(action: () => Promise<void>, after: 'changed' | 'removed') {
  busy.value = true;
  error.value = '';
  try {
    await action();
    if (after === 'removed') emit('removed');
    else emit('changed');
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e);
  } finally {
    busy.value = false;
  }
}

function onAction(action: ContainerAction) {
  if (action === 'kill' && !window.confirm(`Kill "${props.name}"? It gets no chance to shut down cleanly.`)) {
    return;
  }
  void run(() => containerAction(props.env, props.id, action), 'changed');
}

function onRemove() {
  let msg = `Remove "${props.name}"? This cannot be undone.`;
  if (up.value) msg += ' It is running and is killed first.';
  if (volumes.value) msg += ' Its anonymous volumes are removed too.';
  if (!window.confirm(msg)) return;
  void run(
    () => removeContainer(props.env, props.id, { force: up.value, volumes: volumes.value }),
    'removed',
  );
}
</script>

<template>
  <div>
    <div class="actions">
      <template v-if="up">
        <button type="button" :disabled="busy" @click="onAction('stop')">Stop</button>
        <button type="button" :disabled="busy" @click="onAction('restart')">Restart</button>
        <button type="button" class="danger" :disabled="busy" @click="onAction('kill')">Kill</button>
      </template>
      <button v-else type="button" :disabled="busy" @click="onAction('start')">Start</button>
      <button type="button" class="danger" :disabled="busy" @click="onRemove">Remove</button>
      <label v-if="volumesOption" class="volumes">
        <input v-model="volumes" type="checkbox" :disabled="busy" />
        with anonymous volumes
      </label>
    </div>
    <p v-if="error" class="error" role="alert">{{ error }}</p>
  </div>
</template>

<style scoped>
.actions {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 0.4rem;
}

.volumes {
  display: inline-flex;
  align-items: center;
  gap: 0.3rem;
}

.error {
  margin: 0.25rem 0 0;
}
</style>
