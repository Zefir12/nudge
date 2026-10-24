<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue';

import { listAudit, type AuditEntry } from '../api/client';
import { environments, loadEnvironments } from '../environments';
import { formatDate } from '../format';

const entries = ref<AuditEntry[] | null>(null);
const next = ref<number | null>(null);
const error = ref('');
const busy = ref(false);
const filter = reactive({ user: '', environment: '', q: '', result: '' });

const message = (e: unknown) => (e instanceof Error ? e.message : String(e));

const query = computed(() => ({
  user: filter.user.trim(),
  environment: filter.environment,
  q: filter.q.trim(),
  failed: filter.result === '' ? undefined : filter.result === 'failed',
}));

// load fetches the first page, or with more the page after the entries shown.
async function load(more = false) {
  busy.value = true;
  error.value = '';
  try {
    const page = await listAudit({ ...query.value, before: more ? (next.value ?? undefined) : undefined });
    entries.value = more ? [...(entries.value ?? []), ...page.entries] : page.entries;
    next.value = page.next;
  } catch (e) {
    error.value = message(e);
  } finally {
    busy.value = false;
  }
}

function envName(id: string) {
  if (!id) return '—';
  return environments.value?.find((e) => String(e.id) === id)?.name ?? `#${id}`;
}

// The route pattern without the common prefix: "POST /environments/{env}/containers/{id}/start".
const action = (a: string) => a.replace(' /api/v1/', ' /');

onMounted(() => {
  void load();
  // Only for names; the log is readable without them.
  if (!environments.value) loadEnvironments().catch(() => {});
});
</script>

<template>
  <h1>Audit log</h1>
  <p class="muted">Every change made through the API, newest first.</p>

  <section class="card">
    <form class="filters" @submit.prevent="load()">
      <label>
        User
        <input v-model="filter.user" name="user" placeholder="any" />
      </label>
      <label>
        Environment
        <select v-model="filter.environment" name="environment">
          <option value="">any</option>
          <option v-for="e in environments ?? []" :key="e.id" :value="String(e.id)">{{ e.name }}</option>
        </select>
      </label>
      <label>
        Result
        <select v-model="filter.result" name="result">
          <option value="">any</option>
          <option value="failed">failed</option>
          <option value="succeeded">succeeded</option>
        </select>
      </label>
      <label>
        Search
        <input v-model="filter.q" name="q" placeholder="action or target" />
      </label>
      <button type="submit" class="primary" :disabled="busy">Apply</button>
    </form>
    <p v-if="error" class="error" role="alert">{{ error }}</p>

    <p v-if="entries === null && !error" class="muted">Loading…</p>
    <p v-else-if="entries && entries.length === 0" class="muted">No entries.</p>
    <div v-else-if="entries" class="table-wrap">
      <table>
        <thead>
          <tr>
            <th>Time</th>
            <th>User</th>
            <th class="wide-only">Environment</th>
            <th>Action</th>
            <th>Target</th>
            <th>Result</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="e in entries" :key="e.id">
            <td>{{ formatDate(e.time) }}</td>
            <td>
              {{ e.user || '—' }}
              <span v-if="e.token" class="muted">(token {{ e.token }})</span>
            </td>
            <td class="wide-only">{{ envName(e.environment) }}</td>
            <td>
              <code class="break">{{ action(e.action) }}</code>
            </td>
            <td class="break">{{ e.target || '—' }}</td>
            <td :class="e.status >= 400 ? 'failed' : 'ok'">{{ e.status }}</td>
          </tr>
        </tbody>
      </table>
    </div>
    <button v-if="next !== null" type="button" class="more" :disabled="busy" @click="load(true)">
      Load more
    </button>
  </section>
</template>

<style scoped>
.filters {
  display: flex;
  align-items: flex-end;
  gap: 0.75rem;
  flex-wrap: wrap;
}

.filters label {
  display: flex;
  flex-direction: column;
  gap: 0.25rem;
  font-weight: 500;
}

.failed {
  color: var(--danger);
}

.ok {
  color: var(--ok);
}

.more {
  margin-top: 1rem;
}
</style>
