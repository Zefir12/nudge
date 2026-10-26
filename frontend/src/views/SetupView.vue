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

async function onSubmit(c: Credentials) {
  busy.value = true;
  error.value = '';
  try {
    currentUser.value = await setup(c);
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
      <CredentialsForm submit-label="Create admin" confirm :busy="busy" :error="error" @submit="onSubmit" />
    </section>
  </div>
</template>
