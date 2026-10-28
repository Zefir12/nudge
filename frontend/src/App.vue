<script setup lang="ts">
import { computed, watch } from 'vue';
import { RouterLink, RouterView, useRoute, useRouter } from 'vue-router';

import { logout } from './api/client';
import AppBreadcrumbs from './components/AppBreadcrumbs.vue';
import AppSidebar from './components/AppSidebar.vue';
import BrandLogo from './components/BrandLogo.vue';
import { lastEnvironment, rememberEnvironment } from './environments';
import { apiError, currentUser } from './session';

const route = useRoute();
const router = useRouter();

// The environment part of the sidebar follows the open environment, or the one used last.
const env = computed(() => (typeof route.params.env === 'string' ? route.params.env : lastEnvironment()));
watch(
  () => route.params.env,
  (e) => {
    if (typeof e === 'string') rememberEnvironment(e);
  },
  { immediate: true },
);

async function onLogout() {
  let providerLogout = '';
  try {
    providerLogout = (await logout())?.logout_url ?? '';
  } finally {
    currentUser.value = null;
    // An SSO user is signed out at the provider too, which shows its own page.
    if (providerLogout) window.location.assign(providerLogout);
    else await router.replace({ name: 'login' });
  }
}
</script>

<template>
  <div v-if="apiError" class="auth-page">
    <BrandLogo class="auth-logo" />
    <section class="card auth-card" role="alert">
      <h1>nudge is unreachable</h1>
      <p class="error">{{ apiError }}</p>
      <p class="muted">Check that the server is running, then reload the page.</p>
    </section>
  </div>
  <!-- Nothing until the first navigation resolves, so the guard decides the layout. -->
  <template v-else-if="route.matched.length === 0" />
  <RouterView v-else-if="route.meta.public" />
  <div v-else class="shell">
    <header class="topbar">
      <RouterLink to="/" class="brand"><BrandLogo /></RouterLink>
      <AppBreadcrumbs />
    </header>
    <div class="layout">
      <AppSidebar :env="env" @logout="onLogout" />
      <main class="content">
        <RouterView v-if="currentUser?.admin || route.meta.users" />
        <section v-else class="card" role="alert">
          <h1>Not allowed</h1>
          <p class="muted">This page is for admins. Ask an admin if you need access.</p>
        </section>
      </main>
    </div>
  </div>
</template>

<style scoped>
.shell {
  min-height: 100vh;
}

.topbar {
  position: sticky;
  top: 0;
  z-index: 10;
  display: flex;
  align-items: center;
  gap: 1rem;
  height: 56px;
  padding: 0 1.25rem;
  background: var(--topbar);
  backdrop-filter: blur(10px);
}

/* A hairline that fades out at both ends. */
.topbar::after {
  content: '';
  position: absolute;
  right: 0;
  bottom: -1px;
  left: 0;
  height: 1px;
  background: linear-gradient(90deg, transparent, var(--accent-border), transparent);
}

.brand {
  --logo-height: 2.1rem;

  flex-shrink: 0;
  width: calc(248px - 1.25rem - 1rem);
}

.layout {
  display: grid;
  grid-template-columns: 248px minmax(0, 1fr);
  min-height: calc(100vh - 56px);
}

.layout > :first-child {
  position: sticky;
  top: 56px;
  height: calc(100vh - 56px);
  overflow-y: auto;
}

.content {
  padding: 2.25rem 2.5rem 3.5rem;
}

@media (max-width: 720px) {
  .brand {
    width: auto;
  }

  .layout {
    grid-template-columns: minmax(0, 1fr);
  }

  .layout > :first-child {
    position: static;
    height: auto;
  }

  .content {
    padding: 1.25rem 1rem 2rem;
  }
}
</style>
