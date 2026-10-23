<script setup lang="ts">
import { RouterLink, RouterView } from 'vue-router';
</script>

<template>
  <div class="layout">
    <aside class="sidebar">
      <RouterLink to="/" class="brand">nudge</RouterLink>
      <nav>
        <RouterLink to="/">Dashboard</RouterLink>
      </nav>
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

nav a.router-link-exact-active {
  background: var(--accent-soft);
  color: var(--accent);
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
    align-items: center;
    border-right: none;
    border-bottom: 1px solid var(--border);
  }

  nav {
    flex-direction: row;
  }

  .content {
    padding: 1rem;
  }
}
</style>
