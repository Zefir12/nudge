<!-- A deployment's health checks: what makes a run succeed, rollback and the clean-up
     of one-off containers after it. Emits save
     with the settings; the parent saves them. -->
<script setup lang="ts">
import { ref, watch } from 'vue';

import type { DeploymentHealth, DeploymentHttpCheck } from '../api/client';

// MAX_CHECKS matches the server's bound on HTTP checks.
const MAX_CHECKS = 10;

const props = defineProps<{ health: DeploymentHealth; busy: boolean }>();
const emit = defineEmits<{ save: [health: DeploymentHealth] }>();

// A check being edited; key keeps each row's inputs in place when one is removed.
interface CheckRow extends DeploymentHttpCheck {
  key: number;
}

let nextKey = 0;
function row(c?: Partial<DeploymentHttpCheck>): CheckRow {
  return {
    key: nextKey++,
    service: c?.service ?? '',
    port: c?.port ?? 80,
    path: c?.path ?? '/',
    status: c?.status ?? 200,
    timeout_seconds: c?.timeout_seconds ?? 5,
  };
}

const wait = ref(0);
const stable = ref(0);
const checks = ref<CheckRow[]>([]);
const rollback = ref(false);
const removeOneOffs = ref(false);

watch(
  () => props.health,
  (h) => {
    wait.value = h.wait_seconds;
    stable.value = h.stable_seconds;
    checks.value = (h.http_checks ?? []).map((c) => row(c));
    rollback.value = h.rollback;
    removeOneOffs.value = h.remove_oneoffs;
  },
  { immediate: true },
);

function addCheck() {
  if (checks.value.length < MAX_CHECKS) checks.value.push(row());
}

function removeCheck(key: number) {
  checks.value = checks.value.filter((c) => c.key !== key);
}

function onSubmit() {
  emit('save', {
    wait_seconds: wait.value,
    stable_seconds: stable.value,
    http_checks: checks.value.map((c) => ({
      service: c.service.trim(),
      port: c.port,
      path: c.path.trim(),
      status: c.status,
      timeout_seconds: c.timeout_seconds,
    })),
    rollback: rollback.value,
    remove_oneoffs: removeOneOffs.value,
  });
}
</script>

<template>
  <form class="health" @submit.prevent="onSubmit">
    <h3>Health checks</h3>
    <p class="muted">
      A run succeeds only when these pass. Compose waits for the services' healthchecks, then every container
      must keep running without a restart, then each HTTP check runs in order inside its service's network.
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
    <fieldset class="checks">
      <legend>HTTP checks</legend>
      <p v-if="checks.length === 0" class="muted">No HTTP checks.</p>
      <div v-for="(c, i) in checks" :key="c.key" class="row http-check">
        <label>
          Service
          <input v-model="c.service" name="http-service" required placeholder="e.g. web" />
        </label>
        <label>
          Port
          <input v-model.number="c.port" name="http-port" type="number" min="1" max="65535" required />
        </label>
        <label class="wide">
          Path
          <input v-model="c.path" name="http-path" placeholder="/health" />
        </label>
        <label>
          Status
          <input v-model.number="c.status" name="http-status" type="number" min="100" max="599" />
        </label>
        <label>
          Timeout (s)
          <input v-model.number="c.timeout_seconds" name="http-timeout" type="number" min="1" max="60" />
        </label>
        <button
          type="button"
          class="remove"
          :aria-label="`Remove HTTP check ${i + 1}`"
          @click="removeCheck(c.key)"
        >
          Remove
        </button>
      </div>
      <div>
        <button type="button" class="add" :disabled="checks.length >= MAX_CHECKS" @click="addCheck">
          Add HTTP check
        </button>
      </div>
    </fieldset>
    <label class="check">
      <input v-model="rollback" name="rollback" type="checkbox" />
      Roll back when a check fails (redeploys the replaced values; the run still fails)
    </label>
    <label class="check">
      <input v-model="removeOneOffs" name="remove-oneoffs" type="checkbox" />
      Remove finished one-off containers after a successful run (exited with 0, restart "no", such as
      migrations; their logs go with them)
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

.checks {
  display: flex;
  flex-direction: column;
  gap: 0.6rem;
  margin: 0;
  padding: 0.75rem;
  border: 1px solid var(--border);
  border-radius: var(--radius);
}

legend {
  font-weight: 500;
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
