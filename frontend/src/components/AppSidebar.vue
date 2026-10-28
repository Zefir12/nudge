<script setup lang="ts">
import { computed } from 'vue';
import { RouterLink, useRoute } from 'vue-router';

import { environments } from '../environments';
import { currentUser } from '../session';
import EnvSwitcher from './EnvSwitcher.vue';

// AppSidebar has a global part (account, environment picker, pages that are not about one
// environment) and a part for the open environment, or the one used last.
const props = defineProps<{ env: string | null }>();
const emit = defineEmits<{ logout: [] }>();

const route = useRoute();

// Icons are 24×24 stroke paths.
// users: open to users without the admin role; the others are hidden from them.
const globalItems = [
  { to: '/', label: 'Dashboard', exact: true, icon: 'M3 3h7v9H3zM14 3h7v5h-7zM14 12h7v9h-7zM3 16h7v5H3z' },
  { to: '/environments', label: 'Environments', icon: 'M4 4h16v6H4zM4 14h16v6H4zM8 7h.01M8 17h.01' },
  { to: '/deployments', label: 'Deployments', users: true, icon: 'M12 3v12M7 10l5 5 5-5M4 21h16' },
  {
    to: '/registries',
    label: 'Registries',
    icon: 'M4 6c0-1.7 3.6-3 8-3s8 1.3 8 3-3.6 3-8 3-8-1.3-8-3zM4 6v12c0 1.7 3.6 3 8 3s8-1.3 8-3V6M4 12c0 1.7 3.6 3 8 3s8-1.3 8-3',
  },
  {
    to: '/webhooks',
    label: 'Notifications',
    icon: 'M6 8a6 6 0 1 1 12 0c0 7 3 9 3 9H3s3-2 3-9M10.3 21a1.9 1.9 0 0 0 3.4 0',
  },
  {
    to: '/tokens',
    label: 'API tokens',
    users: true,
    icon: 'M7.5 19a4 4 0 1 1 0-8 4 4 0 0 1 0 8zM10.5 12L20 2.5M16 6.5l3 3M13.5 9l2 2',
  },
  {
    to: '/users',
    label: 'Users',
    icon: 'M16 21v-2a4 4 0 0 0-4-4H6a4 4 0 0 0-4 4v2M9 11a4 4 0 1 0 0-8 4 4 0 0 0 0 8zM22 21v-2a4 4 0 0 0-3-3.9M16 3.1a4 4 0 0 1 0 7.8',
  },
  { to: '/audit', label: 'Audit log', icon: 'M9 6h11M9 12h11M9 18h11M4 6h.01M4 12h.01M4 18h.01' },
  { to: '/backup', label: 'Backup', icon: 'M21 8v13H3V8M1 3h22v5H1zM10 12h4' },
];

const isAdmin = computed(() => !!currentUser.value?.admin);
const visibleItems = computed(() => globalItems.filter((item) => isAdmin.value || item.users));

// users: sections open to users without the admin role.
const sections = [
  { name: 'stacks', label: 'Stacks', icon: 'M12 2L2 7l10 5 10-5zM2 17l10 5 10-5M2 12l10 5 10-5' },
  {
    name: 'containers',
    label: 'Containers',
    users: true,
    icon: 'M21 16V8l-9-5-9 5v8l9 5 9-5zM3.3 7L12 12l8.7-5M12 22V12',
  },
  { name: 'images', label: 'Images', icon: 'M3 3h18v18H3zM3 9h18M9 21V9' },
  {
    name: 'volumes',
    label: 'Volumes',
    users: true,
    icon: 'M22 12H2M5.5 5h13L22 12v6a2 2 0 0 1-2 2H4a2 2 0 0 1-2-2v-6zM6 16h.01M10 16h.01',
  },
  {
    name: 'networks',
    label: 'Networks',
    users: true,
    icon: 'M18 8a3 3 0 1 0 0-6 3 3 0 0 0 0 6zM6 15a3 3 0 1 0 0-6 3 3 0 0 0 0 6zM18 22a3 3 0 1 0 0-6 3 3 0 0 0 0 6zM8.6 13.5l6.8 4M15.4 6.5l-6.8 4',
  },
  { name: 'events', label: 'Events', users: true, icon: 'M22 12h-4l-3 9L9 3l-3 9H2' },
];
const visibleSections = computed(() => sections.filter((s) => isAdmin.value || s.users));
const overviewIcon = 'M12 14l4-4M3.3 19a10 10 0 1 1 17.4 0';

const envInfo = computed(() => environments.value?.find((e) => String(e.id) === props.env));

// A section is active on its list and on its detail and form pages (stack, stack-new);
// the tenant form's pages are the containers'.
function sectionActive(name: string) {
  const here = String(route.name ?? '');
  if (!route.params.env) return false;
  return here.startsWith(name.slice(0, -1)) || (name === 'containers' && here.startsWith('tenant-'));
}

// A global page is active on itself and below it (/registries/3).
function globalActive(to: string, exact?: boolean) {
  return exact ? route.path === to : route.path === to || route.path.startsWith(`${to}/`);
}
</script>

<template>
  <aside class="sidebar">
    <div v-if="currentUser" class="account">
      <span class="avatar" aria-hidden="true">{{ currentUser.username.slice(0, 1).toUpperCase() }}</span>
      <RouterLink to="/account" class="who" title="Your account">
        <span class="name">{{ currentUser.username }}</span>
        <span class="role">{{ currentUser.admin ? 'Admin' : 'User' }}</span>
      </RouterLink>
      <button type="button" class="logout" aria-label="Log out" title="Log out" @click="emit('logout')">
        <svg viewBox="0 0 24 24" aria-hidden="true">
          <path d="M9 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h4M16 17l5-5-5-5M21 12H9" />
        </svg>
      </button>
    </div>

    <EnvSwitcher />

    <nav aria-label="Global">
      <span class="group">Global</span>
      <RouterLink
        v-for="item in visibleItems"
        :key="item.to"
        :to="item.to"
        :class="{ active: globalActive(item.to, item.exact) }"
        :aria-current="globalActive(item.to, item.exact) ? 'page' : undefined"
      >
        <svg viewBox="0 0 24 24" aria-hidden="true"><path :d="item.icon" /></svg>
        {{ item.label }}
      </RouterLink>
    </nav>

    <nav v-if="env" class="env" :aria-label="`Environment ${envInfo?.name ?? env}`">
      <span class="group">
        <span class="lamp" :class="{ on: envInfo?.online }" aria-hidden="true" />
        {{ envInfo?.name ?? env }}
      </span>
      <RouterLink
        :to="{ name: 'env', params: { env } }"
        :class="{ active: route.name === 'env' }"
        :aria-current="route.name === 'env' ? 'page' : undefined"
      >
        <svg viewBox="0 0 24 24" aria-hidden="true"><path :d="overviewIcon" /></svg>
        Overview
      </RouterLink>
      <RouterLink
        v-for="s in visibleSections"
        :key="s.name"
        :to="{ name: s.name, params: { env } }"
        :class="{ active: sectionActive(s.name) }"
        :aria-current="sectionActive(s.name) ? 'page' : undefined"
      >
        <svg viewBox="0 0 24 24" aria-hidden="true"><path :d="s.icon" /></svg>
        {{ s.label }}
      </RouterLink>
    </nav>
  </aside>
</template>

<style scoped>
.sidebar {
  display: flex;
  flex-direction: column;
  gap: 1.25rem;
  padding: 0.9rem 0.75rem 1.25rem;
  background: var(--sidebar);
  border-right: 1px solid var(--border);
}

.account {
  display: flex;
  align-items: center;
  gap: 0.6rem;
  padding: 0.45rem 0.5rem 0.45rem 0.6rem;
  border: 1px solid var(--border);
  border-radius: var(--radius-lg);
}

.avatar {
  display: grid;
  place-items: center;
  width: 28px;
  height: 28px;
  font-family: var(--font-display);
  font-size: 13px;
  font-weight: 600;
  color: var(--accent-strong);
  background: var(--accent-soft);
  border: 1px solid var(--accent-border);
  border-radius: var(--radius);
}

.who {
  display: flex;
  flex-direction: column;
  flex-grow: 1;
  min-width: 0;
  line-height: 1.3;
  color: inherit;
  text-decoration: none;
}

.who:hover .name {
  color: var(--accent-strong);
}

.name {
  overflow: hidden;
  font-size: 13px;
  font-weight: 600;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.role {
  font-size: 11.5px;
  color: var(--muted);
}

.logout {
  display: grid;
  place-items: center;
  width: 30px;
  height: 30px;
  padding: 0;
  color: var(--muted);
  background: transparent;
  border-color: transparent;
}

.logout:hover:not(:disabled) {
  color: var(--text);
  border-color: transparent;
}

svg {
  width: 16px;
  height: 16px;
  flex-shrink: 0;
  fill: none;
  stroke: currentColor;
  stroke-width: 1.8;
  stroke-linecap: round;
  stroke-linejoin: round;
}

nav {
  display: flex;
  flex-direction: column;
  gap: 2px;
}

nav.env {
  padding-top: 1rem;
  border-top: 1px solid var(--border);
}

.group {
  display: flex;
  align-items: center;
  gap: 0.5rem;
  padding: 0 0.6rem 0.4rem;
  font-size: 11px;
  font-weight: 600;
  color: var(--muted);
  text-transform: uppercase;
  letter-spacing: 0.14em;
}

.group .lamp {
  width: 6px;
  height: 6px;
}

nav a {
  display: flex;
  align-items: center;
  gap: 0.6rem;
  padding: 0.45rem 0.6rem;
  font-size: 13.5px;
  font-weight: 500;
  color: var(--text-soft);
  text-decoration: none;
  border-radius: var(--radius);
  transition:
    background 0.12s ease,
    color 0.12s ease;
}

nav a:hover {
  color: var(--text);
  background: var(--surface-hover);
}

nav a.active {
  color: var(--accent-strong);
  background: var(--accent-soft);
}

@media (max-width: 720px) {
  .sidebar {
    border-right: none;
    border-bottom: 1px solid var(--border);
  }

  nav {
    flex-direction: row;
    flex-wrap: wrap;
  }

  .group {
    width: 100%;
  }
}
</style>
