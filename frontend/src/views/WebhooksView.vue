<script setup lang="ts">
import { onMounted, ref } from 'vue';

import {
  createWebhook,
  deleteWebhook,
  listWebhooks,
  testWebhook,
  updateWebhook,
  type Webhook,
  type WebhookInput,
} from '../api/client';
import { formatDate } from '../format';

const webhooks = ref<Webhook[] | null>(null);
const error = ref('');
const busy = ref(false);
// editing is the webhook the form changes, null when it adds one.
const editing = ref<Webhook | null>(null);
const form = ref({ name: '', url: '', headerName: '', headerValue: '', enabled: true });
// tested is the outcome of the last test, by webhook id.
const tested = ref<Record<number, { ok: boolean; text: string }>>({});

const message = (e: unknown) => (e instanceof Error ? e.message : String(e));

async function load() {
  try {
    webhooks.value = await listWebhooks();
  } catch (e) {
    error.value = message(e);
  }
}

function reset() {
  editing.value = null;
  form.value = { name: '', url: '', headerName: '', headerValue: '', enabled: true };
}

function onEdit(w: Webhook) {
  editing.value = w;
  form.value = { name: w.name, url: '', headerName: w.header_name, headerValue: '', enabled: w.enabled };
  error.value = '';
}

async function onSubmit() {
  busy.value = true;
  error.value = '';
  const f = form.value;
  const input: WebhookInput = { name: f.name.trim(), header_name: f.headerName.trim(), enabled: f.enabled };
  // On edit an empty URL or header value keeps the stored one.
  if (f.url.trim()) input.url = f.url.trim();
  if (input.header_name && f.headerValue) input.header_value = f.headerValue;
  try {
    if (editing.value) await updateWebhook(editing.value.id, input);
    else await createWebhook(input);
    reset();
    await load();
  } catch (e) {
    error.value = message(e);
  } finally {
    busy.value = false;
  }
}

async function onTest(w: Webhook) {
  error.value = '';
  try {
    const res = await testWebhook(w.id);
    tested.value[w.id] = res.ok
      ? { ok: true, text: 'Test notification delivered.' }
      : { ok: false, text: res.error ?? 'Delivery failed.' };
    await load();
  } catch (e) {
    error.value = message(e);
  }
}

async function onDelete(w: Webhook) {
  if (!window.confirm(`Delete the webhook ${w.name}? It gets no more notifications.`)) return;
  error.value = '';
  try {
    await deleteWebhook(w.id);
    if (editing.value?.id === w.id) reset();
    await load();
  } catch (e) {
    error.value = message(e);
  }
}

onMounted(load);
</script>

<template>
  <h1>Notifications</h1>
  <p class="muted">
    Enabled webhooks get a JSON POST when an image update is found, a container exits with an error or turns
    unhealthy, or an agent stays offline. One problem is sent at most once an hour, each update once. URLs and
    header values are stored encrypted and never shown again.
  </p>

  <section class="card">
    <h2>{{ editing ? `Edit ${editing.name}` : 'Add a webhook' }}</h2>
    <form class="webhook" @submit.prevent="onSubmit">
      <label>
        Name
        <input v-model="form.name" name="name" maxlength="64" placeholder="e.g. Team chat" required />
      </label>
      <label class="url">
        URL
        <input
          v-model="form.url"
          name="url"
          type="url"
          autocomplete="off"
          :placeholder="editing ? `${editing.endpoint}/… (unchanged)` : 'https://hooks.example.com/…'"
          :required="!editing"
        />
      </label>
      <label>
        Secret header
        <input
          v-model="form.headerName"
          name="header_name"
          maxlength="64"
          placeholder="optional, e.g. X-Token"
        />
      </label>
      <label>
        Header value
        <input
          v-model="form.headerValue"
          name="header_value"
          type="password"
          autocomplete="new-password"
          :disabled="!form.headerName.trim()"
          :placeholder="
            editing?.header_name && form.headerName.trim() === editing.header_name ? 'unchanged' : ''
          "
        />
      </label>
      <label class="check">
        <input v-model="form.enabled" name="enabled" type="checkbox" />
        Enabled
      </label>
      <div class="buttons">
        <button type="submit" class="primary" :disabled="busy">{{ editing ? 'Save' : 'Add' }}</button>
        <button v-if="editing" type="button" @click="reset">Cancel</button>
      </div>
    </form>
    <p v-if="error" class="error" role="alert">{{ error }}</p>
  </section>

  <section class="card">
    <p v-if="webhooks === null && !error" class="muted">Loading…</p>
    <p v-else-if="webhooks && webhooks.length === 0" class="muted">No webhooks yet.</p>
    <div v-else-if="webhooks" class="table-wrap">
      <table>
        <thead>
          <tr>
            <th>Name</th>
            <th>Endpoint</th>
            <th>Status</th>
            <th class="wide-only">Last delivery</th>
            <th></th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="w in webhooks" :key="w.id">
            <td>{{ w.name }}</td>
            <td>
              <code>{{ w.endpoint }}</code>
              <span v-if="w.header_name" class="muted"> with {{ w.header_name }}</span>
            </td>
            <td>
              <span :class="w.enabled ? 'on' : 'muted'">{{ w.enabled ? 'Enabled' : 'Disabled' }}</span>
              <p v-if="tested[w.id]" :class="tested[w.id]?.ok ? 'on' : 'error'" role="status">
                {{ tested[w.id]?.text }}
              </p>
            </td>
            <td class="wide-only">
              <template v-if="w.last_sent_at">
                {{ formatDate(w.last_sent_at) }}
                <span v-if="w.last_error" class="error">{{ w.last_error }}</span>
              </template>
              <span v-else class="muted">never</span>
            </td>
            <td class="row-actions">
              <button type="button" @click="onTest(w)">Test</button>
              <button type="button" @click="onEdit(w)">Edit</button>
              <button type="button" class="danger" @click="onDelete(w)">Delete</button>
            </td>
          </tr>
        </tbody>
      </table>
    </div>
  </section>
</template>

<style scoped>
.card + .card {
  margin-top: 1rem;
}

.webhook {
  display: flex;
  align-items: flex-end;
  gap: 0.75rem;
  flex-wrap: wrap;
}

.webhook label {
  display: flex;
  flex-direction: column;
  gap: 0.25rem;
  font-weight: 500;
}

.webhook .url {
  flex: 1 1 18rem;
}

.webhook .check {
  flex-direction: row;
  align-items: center;
}

.on {
  color: var(--ok);
}

td p {
  margin: 0.25rem 0 0;
}

.buttons,
.row-actions {
  display: flex;
  gap: 0.5rem;
}
</style>
