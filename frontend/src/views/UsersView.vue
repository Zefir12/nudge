<script setup lang="ts">
import { onMounted, ref } from 'vue';

import {
  createUser,
  deleteUser,
  getUserEnvironments,
  listEnvironments,
  listUsers,
  setUserEnvironments,
  updateUser,
  type Account,
  type AccountChange,
  type Environment,
  type Role,
} from '../api/client';
import { formatDate } from '../format';
import { currentUser } from '../session';

const users = ref<Account[] | null>(null);
const error = ref('');
const busy = ref(false);

const emptyForm = () => ({ username: '', password: '', role: 'user' as Role });
const form = ref(emptyForm());
// resetting is the user whose new password the reset form takes, null when it is closed.
const resetting = ref<Account | null>(null);
const newPassword = ref('');
// granting is the user whose environments the access form edits, null when it is closed.
const granting = ref<Account | null>(null);
const allEnvironments = ref<Environment[]>([]);
const granted = ref<number[]>([]);

const message = (e: unknown) => (e instanceof Error ? e.message : String(e));

async function load() {
  try {
    users.value = await listUsers();
  } catch (e) {
    error.value = message(e);
  }
}

async function onCreate() {
  busy.value = true;
  error.value = '';
  try {
    await createUser({ ...form.value, username: form.value.username.trim() });
    form.value = emptyForm();
    await load();
  } catch (e) {
    error.value = message(e);
  } finally {
    busy.value = false;
  }
}

async function change(u: Account, c: AccountChange) {
  error.value = '';
  try {
    await updateUser(u.id, c);
  } catch (e) {
    error.value = message(e);
  }
  // Reloaded either way, so a refused role change shows the stored role again.
  await load();
}

function onRole(u: Account, e: Event) {
  void change(u, { role: (e.target as HTMLSelectElement).value as Role });
}

function onDisable(u: Account) {
  if (
    !u.disabled &&
    !window.confirm(`Disable ${u.username}? Their sessions end and their tokens stop working.`)
  ) {
    return;
  }
  void change(u, { disabled: !u.disabled });
}

function onReset(u: Account) {
  resetting.value = u;
  newPassword.value = '';
  error.value = '';
}

async function onResetSubmit() {
  const u = resetting.value;
  if (!u) return;
  busy.value = true;
  error.value = '';
  try {
    await updateUser(u.id, { password: newPassword.value });
    resetting.value = null;
    newPassword.value = '';
  } catch (e) {
    error.value = message(e);
  } finally {
    busy.value = false;
  }
}

async function onEnvironments(u: Account) {
  error.value = '';
  try {
    const [envs, access] = await Promise.all([listEnvironments(), getUserEnvironments(u.id)]);
    allEnvironments.value = envs;
    granted.value = access.environments;
    granting.value = u;
  } catch (e) {
    error.value = message(e);
  }
}

async function onEnvironmentsSubmit() {
  const u = granting.value;
  if (!u) return;
  busy.value = true;
  error.value = '';
  try {
    await setUserEnvironments(u.id, granted.value);
    granting.value = null;
  } catch (e) {
    error.value = message(e);
  } finally {
    busy.value = false;
  }
}

async function onDelete(u: Account) {
  if (!window.confirm(`Delete ${u.username}? Their sessions, tokens and webhooks go with them.`)) return;
  error.value = '';
  try {
    await deleteUser(u.id);
    if (resetting.value?.id === u.id) resetting.value = null;
    if (granting.value?.id === u.id) granting.value = null;
    await load();
  } catch (e) {
    error.value = message(e);
  }
}

onMounted(load);
</script>

<template>
  <h1>Users</h1>
  <p class="muted">
    Admins can do everything; users only what is open to them: the deployments they are a member of, and in
    the environments given to them only their own containers, volumes and networks. SSO users get their role
    from the provider on every login, so here they can only be disabled.
  </p>

  <section class="card">
    <h2>Add a user</h2>
    <form class="user" @submit.prevent="onCreate">
      <label>
        Username
        <input v-model="form.username" name="username" maxlength="64" autocomplete="off" required />
      </label>
      <label>
        Password
        <input
          v-model="form.password"
          name="password"
          type="password"
          minlength="8"
          autocomplete="new-password"
          required
        />
      </label>
      <label>
        Role
        <select v-model="form.role" name="role">
          <option value="user">User</option>
          <option value="admin">Admin</option>
        </select>
      </label>
      <button type="submit" class="primary" :disabled="busy">Add</button>
    </form>
    <form v-if="resetting" class="user reset" @submit.prevent="onResetSubmit">
      <label>
        New password for {{ resetting.username }}
        <input
          v-model="newPassword"
          name="new_password"
          type="password"
          minlength="8"
          autocomplete="new-password"
          required
        />
      </label>
      <button type="submit" class="primary" :disabled="busy">Reset password</button>
      <button type="button" @click="resetting = null">Cancel</button>
    </form>
    <form v-if="granting" class="user access" @submit.prevent="onEnvironmentsSubmit">
      <fieldset>
        <legend>Environments {{ granting.username }} may use</legend>
        <p v-if="allEnvironments.length === 0" class="muted">There are no environments yet.</p>
        <label v-for="env in allEnvironments" :key="env.id" class="check">
          <input v-model="granted" type="checkbox" :value="env.id" :name="`env-${env.id}`" />
          {{ env.name }}
        </label>
      </fieldset>
      <button type="submit" class="primary" :disabled="busy">Save</button>
      <button type="button" @click="granting = null">Cancel</button>
    </form>
    <p v-if="error" class="error" role="alert">{{ error }}</p>
  </section>

  <section class="card">
    <p v-if="users === null && !error" class="muted">Loading…</p>
    <div v-else-if="users" class="table-wrap">
      <table>
        <thead>
          <tr>
            <th>Username</th>
            <th class="wide-only">Name</th>
            <th>Role</th>
            <th>Status</th>
            <th class="wide-only">Created</th>
            <th></th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="u in users" :key="u.id" :class="{ disabled: u.disabled }">
            <td>
              {{ u.username }}
              <span v-if="u.sso" class="chip">SSO</span>
              <span v-if="u.id === currentUser?.id" class="muted">(you)</span>
            </td>
            <td class="wide-only">{{ u.display_name || u.email }}</td>
            <td>
              <select
                :value="u.role"
                :name="`role-${u.id}`"
                :aria-label="`Role of ${u.username}`"
                :disabled="u.sso"
                :title="u.sso ? 'Set by the SSO provider' : undefined"
                @change="onRole(u, $event)"
              >
                <option value="user">User</option>
                <option value="admin">Admin</option>
              </select>
            </td>
            <td>{{ u.disabled ? 'Disabled' : 'Active' }}</td>
            <td class="wide-only">{{ formatDate(u.created_at) }}</td>
            <td class="row-actions">
              <button v-if="u.role === 'user'" type="button" @click="onEnvironments(u)">Environments</button>
              <button v-if="!u.sso" type="button" @click="onReset(u)">Reset password</button>
              <button type="button" @click="onDisable(u)">{{ u.disabled ? 'Enable' : 'Disable' }}</button>
              <button type="button" class="danger" @click="onDelete(u)">Delete</button>
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

.user {
  display: flex;
  align-items: flex-end;
  gap: 0.75rem;
  flex-wrap: wrap;
}

.user + .user {
  margin-top: 1rem;
}

.user fieldset {
  display: flex;
  flex-wrap: wrap;
  gap: 0.75rem;
  border: 0;
  margin: 0;
  padding: 0;
}

.user legend {
  font-weight: 500;
  margin-bottom: 0.25rem;
}

.user label.check {
  flex-direction: row;
  align-items: center;
  font-weight: 400;
}

.user label {
  display: flex;
  flex-direction: column;
  gap: 0.25rem;
  font-weight: 500;
}

tr.disabled td:first-child {
  color: var(--muted);
}

.row-actions {
  display: flex;
  gap: 0.5rem;
}
</style>
