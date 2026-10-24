<script setup lang="ts">
import { ref } from 'vue';
import { useRoute, useRouter } from 'vue-router';

import { login, type Credentials } from '../api/client';
import CredentialsForm from '../components/CredentialsForm.vue';
import { currentUser, safeRedirect } from '../session';

const route = useRoute();
const router = useRouter();
const busy = ref(false);
const error = ref('');

async function onSubmit(c: Credentials) {
  busy.value = true;
  error.value = '';
  try {
    currentUser.value = await login(c);
    await router.replace(safeRedirect(route.query.redirect));
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e);
  } finally {
    busy.value = false;
  }
}
</script>

<template>
  <div class="auth-page">
    <section class="card auth-card">
      <h1>Log in to nudge</h1>
      <CredentialsForm submit-label="Log in" :busy="busy" :error="error" @submit="onSubmit" />
    </section>
  </div>
</template>
