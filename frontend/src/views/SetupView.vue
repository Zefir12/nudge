<script setup lang="ts">
import { ref } from 'vue';
import { useRouter } from 'vue-router';

import { ApiError, setup, type Credentials } from '../api/client';
import BrandLogo from '../components/BrandLogo.vue';
import CredentialsForm from '../components/CredentialsForm.vue';
import { currentUser } from '../session';

const router = useRouter();
const busy = ref(false);
const error = ref('');
const token = ref('');

async function onSubmit(c: Credentials) {
  busy.value = true;
  error.value = '';
  try {
    currentUser.value = await setup({ ...c, token: token.value.trim() });
    await router.replace({ name: 'dashboard' });
  } catch (e) {
    if (e instanceof ApiError && e.status === 409) {
      // Someone else finished setup first.
      await router.replace({ name: 'login' });
      return;
    }
    error.value = e instanceof Error ? e.message : String(e);
  } finally {
    busy.value = false;
  }
}
</script>

<template>
  <div class="auth-page">
    <BrandLogo class="auth-logo" />
    <section class="card auth-card">
      <h1>Welcome to nudge</h1>
      <p class="muted">Create the admin account. You will use it to log in.</p>
      <CredentialsForm submit-label="Create admin" confirm :busy="busy" :error="error" @submit="onSubmit">
        <label class="token">
          Setup token
          <input v-model="token" name="token" autocomplete="off" spellcheck="false" required />
          <span class="muted hint">
            The server logs it at start (<code>docker compose logs nudge</code>), so only you can create the
            admin.
          </span>
        </label>
      </CredentialsForm>
    </section>
  </div>
</template>

<style scoped>
.token {
  display: flex;
  flex-direction: column;
  gap: 0.25rem;
  font-weight: 500;
}

.hint {
  font-weight: 400;
  font-size: 0.875rem;
}
</style>
