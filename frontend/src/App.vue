<script setup lang="ts">
import { computed, watch } from 'vue';
import { RouterLink, RouterView, useRoute, useRouter } from 'vue-router';

import { logout } from './api/client';
import EnvSwitcher from './components/EnvSwitcher.vue';
import { lastEnvironment, rememberEnvironment } from './environments';
import { apiError, currentUser } from './session';

const route = useRoute();
const router = useRouter();

// The environment links follow the open environment, or the one used last.
const env = computed(() => (typeof route.params.env === 'string' ? route.params.env : lastEnvironment()));
watch(
  () => route.params.env,
  (e) => {
    if (typeof e === 'string') rememberEnvironment(e);
  },
  { immediate: true },
);

const sections = [
  { name: 'containers', label: 'Containers' },
  { name: 'images', label: 'Images' },
  { name: 'volumes', label: 'Volumes' },
  { name: 'networks', label: 'Networks' },
];

async function onLogout() {
  try {
    await logout();
  } finally {
    currentUser.value = null;
    await router.replace({ name: 'login' });
  }
}
</script>

<template>
  <div v-if="apiError" class="auth-page">
    <section class="card auth-card" role="alert">
      <h1>nudge is unreachable</h1>
      <p class="error">{{ apiError }}</p>
      <p class="muted">Check that the server is running, then reload the page.</p>
    </section>
  </div>
  <!-- Nothing until the first navigation resolves, so the guard decides the layout. -->
  <template v-else-if="route.matched.length === 0" />
  <RouterView v-else-if="route.meta.public" />
  <div v-else class="layout">
    <aside class="sidebar">
      <RouterLink to="/" class="brand">nudge</RouterLink>
      <EnvSwitcher />
      <nav>
        <template v-if="env">
          <RouterLink :to="{ name: 'env', params: { env } }">Dashboard</RouterLink>
          <!-- Active on a list and on its detail pages. -->
          <RouterLink
            v-for="s in sections"
            :key="s.name"
            :to="{ name: s.name, params: { env } }"
            :class="{ active: route.name === s.name || route.name === s.name.slice(0, -1) }"
          >
            {{ s.label }}
          </RouterLink>
        </template>
        <RouterLink v-else to="/">Dashboard</RouterLink>
        <RouterLink to="/environments">Environments</RouterLink>
        <RouterLink to="/registries">Registries</RouterLink>
        <RouterLink to="/tokens">API tokens</RouterLink>
      </nav>
      <div v-if="currentUser" class="account">
        <span class="muted">{{ currentUser.username }}</span>
        <button type="button" @click="onLogout">Log out</button>
      </div>
    </aside>
    <main class="content">
      <RouterView />
    </main>
  </div>
</template>

<style scoped>
.layout {
  display: grid;
  grid-template-columns: 220px 1fr;
  min-height: 100vh;
}

.sidebar {
  display: flex;
  flex-direction: column;
  gap: 1.5rem;
  padding: 1.25rem 1rem;
  background: var(--surface);
  border-right: 1px solid var(--border);
}

.brand {
  font-size: 1.25rem;
  font-weight: 700;
  color: var(--accent);
  text-decoration: none;
}

nav {
  display: flex;
  flex-direction: column;
  gap: 0.25rem;
}

nav a {
  padding: 0.5rem 0.75rem;
  border-radius: var(--radius);
  color: var(--text);
  text-decoration: none;
}

nav a.router-link-exact-active,
nav a.active {
  background: var(--accent-soft);
  color: var(--accent);
}

.account {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 0.5rem;
  margin-top: auto;
}

.content {
  padding: 1.5rem 2rem;
}

@media (max-width: 720px) {
  .layout {
    grid-template-columns: 1fr;
  }

  .sidebar {
    flex-direction: row;
    flex-wrap: wrap;
    align-items: center;
    gap: 0.75rem 1rem;
    border-right: none;
    border-bottom: 1px solid var(--border);
  }

  nav {
    flex-direction: row;
    flex-wrap: wrap;
    order: 3;
    width: 100%;
  }

  .account {
    margin-top: 0;
    margin-left: auto;
  }

  .content {
    padding: 1rem;
  }
}
</style>
