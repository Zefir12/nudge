<!-- A deployment's health checks: what makes a run succeed, and rollback. Emits save
     with the settings; the parent saves them. -->
<script setup lang="ts">
import { ref, watch } from 'vue';

import type { DeploymentHealth } from '../api/client';

const props = defineProps<{ health: DeploymentHealth; busy: boolean }>();
const emit = defineEmits<{ save: [health: DeploymentHealth] }>();

const wait = ref(0);
const stable = ref(0);
const httpOn = ref(false);
const service = ref('');
const port = ref(80);
const path = ref('/');
const status = ref(200);
const timeout = ref(5);
const rollback = ref(false);

watch(
  () => props.health,
  (h) => {
    wait.value = h.wait_seconds;
    stable.value = h.stable_seconds;
    httpOn.value = !!h.http;
    service.value = h.http?.service ?? '';
    port.value = h.http?.port ?? 80;
    path.value = h.http?.path ?? '/';
    status.value = h.http?.status ?? 200;
    timeout.value = h.http?.timeout_seconds ?? 5;
    rollback.value = h.rollback;
  },
  { immediate: true },
);

function onSubmit() {
  emit('save', {
    wait_seconds: wait.value,
    stable_seconds: stable.value,
    http: httpOn.value
      ? {
          service: service.value.trim(),
          port: port.value,
          path: path.value.trim(),
          status: status.value,
          timeout_seconds: timeout.value,
        }
      : undefined,
    rollback: rollback.value,
  });
}
</script>

<template>
  <form class="health" @submit.prevent="onSubmit">
    <h3>Health checks</h3>
    <p class="muted">
      A run succeeds only when these pass. Compose waits for the services' healthchecks, then every container
      must keep running without a restart, then the HTTP check runs inside the service's network.
    </p>
    <div class="row">
      <label>
        Wait for healthy (s)
        <input v-model.number="wait" name="wait" type="number" min="0" max="1800" />
      </label>
      <label>
        Stability window (s)
        <input v-model.number="stable" name="stable" type="number" min="0" max="600" />
      </label>
    </div>
    <label class="check"><input v-model="httpOn" name="http" type="checkbox" /> HTTP check</label>
    <div v-if="httpOn" class="row">
      <label>
        Service
        <input v-model="service" name="http-service" required placeholder="e.g. web" />
      </label>
      <label>
        Port
        <input v-model.number="port" name="http-port" type="number" min="1" max="65535" required />
      </label>
      <label class="wide">
        Path
        <input v-model="path" name="http-path" placeholder="/health" />
      </label>
      <label>
        Status
        <input v-model.number="status" name="http-status" type="number" min="100" max="599" />
      </label>
      <label>
        Timeout (s)
        <input v-model.number="timeout" name="http-timeout" type="number" min="1" max="60" />
      </label>
    </div>
    <label class="check">
      <input v-model="rollback" name="rollback" type="checkbox" />
      Roll back when a check fails (redeploys the replaced values; the run still fails)
    </label>
    <div>
      <button type="submit" :disabled="busy">Save health checks</button>
    </div>
  </form>
</template>

<style scoped>
.health {
  display: flex;
  flex-direction: column;
  gap: 0.6rem;
  margin: 1.25rem 0;
}

h3,
p {
  margin: 0;
}

.row {
  display: flex;
  align-items: flex-end;
  gap: 0.75rem;
  flex-wrap: wrap;
}

label {
  display: flex;
  flex-direction: column;
  gap: 0.25rem;
  font-weight: 500;
}

.row input[type='number'] {
  width: 8rem;
}

.check {
  flex-direction: row;
  align-items: center;
  gap: 0.5rem;
  font-weight: normal;
}

.wide {
  flex: 1 1 12rem;
}
</style>
