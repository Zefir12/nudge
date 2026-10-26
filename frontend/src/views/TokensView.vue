<script setup lang="ts">
import { onMounted, ref } from 'vue';

import { createToken, deleteToken, listTokens, type CreatedToken, type Token } from '../api/client';

const tokens = ref<Token[] | null>(null);
const error = ref('');
const name = ref('');
const busy = ref(false);
// created holds the new token's secret until the user dismisses it; it is never shown again.
const created = ref<CreatedToken | null>(null);
const copyState = ref<'' | 'copied' | 'failed'>('');

const message = (e: unknown) => (e instanceof Error ? e.message : String(e));

async function load() {
  try {
    tokens.value = await listTokens();
  } catch (e) {
    error.value = message(e);
  }
}

async function onCreate() {
  busy.value = true;
  error.value = '';
  try {
    created.value = await createToken(name.value.trim());
    copyState.value = '';
    name.value = '';
    await load();
  } catch (e) {
    error.value = message(e);
  } finally {
    busy.value = false;
  }
}

async function onRevoke(t: Token) {
  if (!window.confirm(`Revoke the token "${t.name}"? Scripts using it stop working.`)) return;
  error.value = '';
  try {
    await deleteToken(t.id);
    if (created.value?.id === t.id) created.value = null;
    await load();
  } catch (e) {
    error.value = message(e);
  }
}

async function onCopy() {
  if (!created.value) return;
  try {
    await navigator.clipboard.writeText(created.value.token);
    copyState.value = 'copied';
  } catch {
    // No clipboard API (plain HTTP): the token stays selectable.
    copyState.value = 'failed';
  }
}

const formatDate = (iso: string | null) => (iso ? new Date(iso).toLocaleString() : 'never');

onMounted(load);
</script>

<template>
  <h1>API tokens</h1>
  <p class="muted">
    Scripts call the API with <code>Authorization: Bearer &lt;token&gt;</code>. A token acts as you on every
    route.
  </p>

  <section v-if="created" class="card created" aria-live="polite">
    <h2>Token "{{ created.name }}" created</h2>
    <p>Copy it now: it is shown only once.</p>
    <code class="secret">{{ created.token }}</code>
    <div class="actions">
      <button type="button" @click="onCopy">{{ copyState === 'copied' ? 'Copied' : 'Copy' }}</button>
      <button type="button" @click="created = null">Done</button>
    </div>
    <p v-if="copyState === 'failed'" class="muted">
      Copying is not available here: select the token and copy it.
    </p>
  </section>

  <section class="card">
    <form class="create" @submit.prevent="onCreate">
      <label>
        Name
        <input v-model="name" name="name" maxlength="64" placeholder="e.g. backup script" required />
      </label>
      <button type="submit" class="primary" :disabled="busy">Create token</button>
    </form>
    <p v-if="error" class="error" role="alert">{{ error }}</p>

    <p v-if="tokens === null && !error" class="muted">Loading…</p>
    <p v-else-if="tokens && tokens.length === 0" class="muted">No tokens yet.</p>
    <table v-else-if="tokens">
      <thead>
        <tr>
          <th>Name</th>
          <th>Token</th>
          <th>Created</th>
          <th>Last used</th>
          <th></th>
        </tr>
      </thead>
      <tbody>
        <tr v-for="t in tokens" :key="t.id">
          <td>{{ t.name }}</td>
          <td>
            <code>{{ t.prefix }}…</code>
          </td>
          <td>{{ formatDate(t.created_at) }}</td>
          <td>{{ formatDate(t.last_used_at) }}</td>
          <td><button type="button" class="danger" @click="onRevoke(t)">Revoke</button></td>
        </tr>
      </tbody>
    </table>
  </section>
</template>

<style scoped>
.card + .card {
  margin-top: 1rem;
}

.created {
  border-color: var(--accent);
}

.secret {
  display: block;
  padding: 0.5rem 0.75rem;
  background: var(--bg);
  border-radius: var(--radius);
  word-break: break-all;
  user-select: all;
}

.actions {
  display: flex;
  gap: 0.5rem;
  margin-top: 0.75rem;
}

.create {
  display: flex;
  align-items: flex-end;
  gap: 0.75rem;
  flex-wrap: wrap;
}

.create label {
  display: flex;
  flex-direction: column;
  gap: 0.25rem;
  font-weight: 500;
}
</style>
