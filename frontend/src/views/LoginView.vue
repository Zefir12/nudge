<script setup lang="ts">
import { computed, onMounted, ref } from 'vue';
import { useRoute, useRouter } from 'vue-router';

import { getAuthMethods, login, ssoLoginUrl, type AuthMethods, type Credentials } from '../api/client';
import BrandLogo from '../components/BrandLogo.vue';
import CredentialsForm from '../components/CredentialsForm.vue';
import { currentUser, safeRedirect } from '../session';

const route = useRoute();
const router = useRouter();
const busy = ref(false);
const error = ref('');
// Until the methods load (or if they can't), the password form shows as before.
const methods = ref<AuthMethods>({ password: true, oidc: null });

const redirect = computed(() => safeRedirect(route.query.redirect));
// ?password shows the form when password login is off: the break-glass admin's way in.
const showPassword = computed(() => methods.value.password || route.query.password !== undefined);
const ssoError = computed(() =>
  route.query.error === 'sso' ? 'Single sign-on failed. Try again, or ask an administrator.' : '',
);

onMounted(async () => {
  try {
    methods.value = await getAuthMethods();
  } catch {
    // An older server without the endpoint: password only.
  }
});

async function onSubmit(c: Credentials) {
  busy.value = true;
  error.value = '';
  try {
    currentUser.value = await login(c);
    await router.replace(redirect.value);
  } catch (e) {
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
      <h1>Log in to nudge</h1>
      <p v-if="ssoError" class="error" role="alert">{{ ssoError }}</p>
      <a v-if="methods.oidc" class="button primary sso" :href="ssoLoginUrl(redirect)">
        Log in with {{ methods.oidc.name }}
      </a>
      <p v-if="methods.oidc && showPassword" class="muted divider">or</p>
      <CredentialsForm
        v-if="showPassword"
        submit-label="Log in"
        :busy="busy"
        :error="error"
        @submit="onSubmit"
      />
    </section>
  </div>
</template>

<style scoped>
.sso {
  display: block;
  text-align: center;
}

.divider {
  margin: 0.75rem 0;
  text-align: center;
}
</style>
