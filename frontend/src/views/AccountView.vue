<script setup lang="ts">
import { computed, ref } from 'vue';

import { changePassword } from '../api/client';
import { currentUser } from '../session';

// Matches the server's minimum.
const minLength = 8;

const current = ref('');
const next = ref('');
const repeat = ref('');
const busy = ref(false);
const error = ref('');
const done = ref(false);

const mismatch = computed(() => repeat.value !== '' && repeat.value !== next.value);
const ready = computed(
  () => current.value !== '' && next.value.length >= minLength && repeat.value === next.value && !busy.value,
);

async function onChange() {
  busy.value = true;
  error.value = '';
  done.value = false;
  try {
    await changePassword(current.value, next.value);
    done.value = true;
    current.value = '';
    next.value = '';
    repeat.value = '';
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e);
  } finally {
    busy.value = false;
  }
}
</script>

<template>
  <h1>Account</h1>
  <p v-if="currentUser" class="muted">
    Signed in as <strong>{{ currentUser.username }}</strong> ({{ currentUser.admin ? 'admin' : 'user' }}).
  </p>

  <section class="card">
    <h2>Password</h2>
    <p v-if="currentUser?.sso" class="muted">
      You sign in through single sign-on: your password is managed by your identity provider.
    </p>
    <template v-else>
      <p>Changing it signs you out everywhere else. API tokens keep working.</p>
      <form class="password" @submit.prevent="onChange">
        <label>
          Current password
          <input v-model="current" name="current" type="password" autocomplete="current-password" required />
        </label>
        <label>
          New password
          <input
            v-model="next"
            name="new"
            type="password"
            autocomplete="new-password"
            :minlength="minLength"
            required
          />
        </label>
        <label>
          Repeat it
          <input v-model="repeat" name="repeat" type="password" autocomplete="new-password" required />
        </label>
        <button type="submit" class="primary" :disabled="!ready">Change password</button>
      </form>
      <p class="muted">At least {{ minLength }} characters.</p>
      <p v-if="mismatch" class="error">The new passwords differ.</p>
      <p v-if="error" class="error" role="alert">{{ error }}</p>
      <p v-if="done" aria-live="polite">Password changed. Your other sessions have ended.</p>
    </template>
  </section>
</template>

<style scoped>
.password {
  display: flex;
  align-items: flex-end;
  gap: 0.75rem;
  flex-wrap: wrap;
}

.password label {
  display: flex;
  flex-direction: column;
  gap: 0.25rem;
  font-weight: 500;
}
</style>
