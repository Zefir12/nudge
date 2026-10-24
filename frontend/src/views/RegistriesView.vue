<script setup lang="ts">
import { onMounted, ref } from 'vue';

import {
  createRegistry,
  deleteRegistry,
  listRegistries,
  updateRegistry,
  type Registry,
  type RegistryInput,
} from '../api/client';
import { formatDate } from '../format';

const registries = ref<Registry[] | null>(null);
const error = ref('');
const busy = ref(false);
// editing is the registry the form changes, null when it adds one.
const editing = ref<Registry | null>(null);
const form = ref({ name: '', address: '', username: '', password: '' });

const message = (e: unknown) => (e instanceof Error ? e.message : String(e));

async function load() {
  try {
    registries.value = await listRegistries();
  } catch (e) {
    error.value = message(e);
  }
}

function reset() {
  editing.value = null;
  form.value = { name: '', address: '', username: '', password: '' };
}

function onEdit(r: Registry) {
  editing.value = r;
  form.value = { name: r.name, address: r.address, username: r.username, password: '' };
  error.value = '';
}

async function onSubmit() {
  busy.value = true;
  error.value = '';
  const f = form.value;
  const input: RegistryInput = {
    name: f.name.trim(),
    address: f.address.trim(),
    username: f.username.trim(),
  };
  // An empty password on edit keeps the stored one.
  if (f.password) input.password = f.password;
  try {
    if (editing.value) await updateRegistry(editing.value.id, input);
    else await createRegistry(input);
    reset();
    await load();
  } catch (e) {
    error.value = message(e);
  } finally {
    busy.value = false;
  }
}

async function onDelete(r: Registry) {
  if (!window.confirm(`Delete the credentials for ${r.address}? Pulls from it become anonymous.`)) return;
  error.value = '';
  try {
    await deleteRegistry(r.id);
    if (editing.value?.id === r.id) reset();
    await load();
  } catch (e) {
    error.value = message(e);
  }
}

onMounted(load);
</script>

<template>
  <h1>Registries</h1>
  <p class="muted">
    Image pulls from a registry listed here use its credentials, matched by the image's host (Docker Hub for
    images without one). Passwords are stored encrypted and never shown again.
  </p>

  <section class="card">
    <h2>{{ editing ? `Edit ${editing.name}` : 'Add a registry' }}</h2>
    <form class="registry" @submit.prevent="onSubmit">
      <label>
        Name
        <input v-model="form.name" name="name" maxlength="64" placeholder="e.g. GitHub" required />
      </label>
      <label>
        Address
        <input v-model="form.address" name="address" placeholder="ghcr.io or docker.io" required />
      </label>
      <label>
        Username
        <input v-model="form.username" name="username" maxlength="255" autocomplete="off" required />
      </label>
      <label>
        Password or token
        <input
          v-model="form.password"
          name="password"
          type="password"
          autocomplete="new-password"
          :placeholder="editing ? 'unchanged' : ''"
          :required="!editing"
        />
      </label>
      <div class="buttons">
        <button type="submit" class="primary" :disabled="busy">{{ editing ? 'Save' : 'Add' }}</button>
        <button v-if="editing" type="button" @click="reset">Cancel</button>
      </div>
    </form>
    <p v-if="error" class="error" role="alert">{{ error }}</p>
  </section>

  <section class="card">
    <p v-if="registries === null && !error" class="muted">Loading…</p>
    <p v-else-if="registries && registries.length === 0" class="muted">No registries yet.</p>
    <div v-else-if="registries" class="table-wrap">
      <table>
        <thead>
          <tr>
            <th>Name</th>
            <th>Address</th>
            <th>Username</th>
            <th class="wide-only">Updated</th>
            <th></th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="r in registries" :key="r.id">
            <td>{{ r.name }}</td>
            <td>
              <code>{{ r.address }}</code>
            </td>
            <td>{{ r.username }}</td>
            <td class="wide-only">{{ formatDate(r.updated_at) }}</td>
            <td class="row-actions">
              <button type="button" @click="onEdit(r)">Edit</button>
              <button type="button" class="danger" @click="onDelete(r)">Delete</button>
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

.registry {
  display: flex;
  align-items: flex-end;
  gap: 0.75rem;
  flex-wrap: wrap;
}

.registry label {
  display: flex;
  flex-direction: column;
  gap: 0.25rem;
  font-weight: 500;
}

.buttons,
.row-actions {
  display: flex;
  gap: 0.5rem;
}
</style>
