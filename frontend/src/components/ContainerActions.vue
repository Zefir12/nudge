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
  // icons shows each action as an icon button, named by its tooltip (for table rows).
  icons?: boolean;
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

const paths = {
  start: 'M7 4.5v15l12-7.5z',
  stop: 'M5.5 5.5h13v13h-13z',
  restart: 'M3.5 12a8.5 8.5 0 1 0 2.5-6L3.5 8.5M3.5 3.5v5h5',
  kill: 'M12 2.5a7.5 7.5 0 0 0-7.5 7.5c0 2.6 1.2 4.4 3 5.5v3a1 1 0 0 0 1 1h7a1 1 0 0 0 1-1v-3c1.8-1.1 3-2.9 3-5.5A7.5 7.5 0 0 0 12 2.5zM9 13a1.5 1.5 0 1 0 0-3 1.5 1.5 0 0 0 0 3M15 13a1.5 1.5 0 1 0 0-3 1.5 1.5 0 0 0 0 3M10.5 19.5v-2M13.5 19.5v-2',
  remove: 'M4 6.5h16M9 6.5V4h6v2.5M18 6.5l-.9 13.5H6.9L6 6.5M10 10.5v6M14 10.5v6',
};

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
        <button
          type="button"
          class="stop"
          :class="{ icon: props.icons }"
          :disabled="busy"
          title="Stop"
          :aria-label="props.icons ? 'Stop' : undefined"
          @click="onAction('stop')"
        >
          <svg v-if="props.icons" viewBox="0 0 24 24" aria-hidden="true"><path :d="paths.stop" /></svg>
          <template v-else>Stop</template>
        </button>
        <button
          type="button"
          class="restart"
          :class="{ icon: props.icons }"
          :disabled="busy"
          title="Restart"
          :aria-label="props.icons ? 'Restart' : undefined"
          @click="onAction('restart')"
        >
          <svg v-if="props.icons" viewBox="0 0 24 24" aria-hidden="true"><path :d="paths.restart" /></svg>
          <template v-else>Restart</template>
        </button>
        <button
          type="button"
          class="danger kill"
          :class="{ icon: props.icons }"
          :disabled="busy"
          title="Kill"
          :aria-label="props.icons ? 'Kill' : undefined"
          @click="onAction('kill')"
        >
          <svg v-if="props.icons" viewBox="0 0 24 24" aria-hidden="true"><path :d="paths.kill" /></svg>
          <template v-else>Kill</template>
        </button>
      </template>
      <button
        v-else
        type="button"
        class="start"
        :class="{ icon: props.icons }"
        :disabled="busy"
        title="Start"
        :aria-label="props.icons ? 'Start' : undefined"
        @click="onAction('start')"
      >
        <svg v-if="props.icons" viewBox="0 0 24 24" aria-hidden="true"><path :d="paths.start" /></svg>
        <template v-else>Start</template>
      </button>
      <button
        type="button"
        class="danger remove"
        :class="{ icon: props.icons }"
        :disabled="busy"
        title="Remove"
        :aria-label="props.icons ? 'Remove' : undefined"
        @click="onRemove"
      >
        <svg v-if="props.icons" viewBox="0 0 24 24" aria-hidden="true"><path :d="paths.remove" /></svg>
        <template v-else>Remove</template>
      </button>
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

.actions:has(.icon) {
  flex-wrap: nowrap;
  gap: 0.15rem;
}

.icon {
  --tone: var(--accent-strong);
  --tone-soft: var(--accent-soft);
  --tone-border: var(--accent-border);

  display: grid;
  place-items: center;
  width: 28px;
  height: 28px;
  padding: 0;
  color: var(--tone);
  background: transparent;
  border-color: transparent;
}

/* Each action has its own colour so a row reads at a glance; Remove stays grey until hovered. */
.icon.start {
  --tone: var(--ok-text);
  --tone-soft: var(--ok-soft);
  --tone-border: rgb(86 211 100 / 35%);
}

.icon.stop {
  --tone: var(--warn);
  --tone-soft: var(--warn-soft);
  --tone-border: rgb(212 167 44 / 40%);
}

.icon.kill,
.icon.remove {
  --tone: var(--danger-text);
  --tone-soft: var(--danger-soft);
  --tone-border: rgb(240 96 90 / 40%);
}

.icon.remove {
  color: var(--muted);
}

.icon:hover:not(:disabled) {
  color: var(--tone);
  background: var(--tone-soft);
  border-color: var(--tone-border);
}

.icon svg {
  width: 16px;
  height: 16px;
  fill: none;
  stroke: currentColor;
  stroke-width: 1.8;
  stroke-linecap: round;
  stroke-linejoin: round;
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
