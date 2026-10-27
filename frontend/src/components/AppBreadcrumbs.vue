<script setup lang="ts">
import { computed } from 'vue';
import { RouterLink, useRoute, type RouteLocationRaw } from 'vue-router';

import { environments } from '../environments';

// AppBreadcrumbs shows where a page sits: the dashboard, the environment, the section.
const route = useRoute();

// The section each route belongs to; its list route is where the crumb links.
const sectionOf: Record<string, { label: string; list: string }> = {
  containers: { label: 'Containers', list: 'containers' },
  'container-new': { label: 'Containers', list: 'containers' },
  container: { label: 'Containers', list: 'containers' },
  'container-edit': { label: 'Containers', list: 'containers' },
  images: { label: 'Images', list: 'images' },
  image: { label: 'Images', list: 'images' },
  volumes: { label: 'Volumes', list: 'volumes' },
  volume: { label: 'Volumes', list: 'volumes' },
  networks: { label: 'Networks', list: 'networks' },
  network: { label: 'Networks', list: 'networks' },
  stacks: { label: 'Stacks', list: 'stacks' },
  'stack-new': { label: 'Stacks', list: 'stacks' },
  stack: { label: 'Stacks', list: 'stacks' },
  events: { label: 'Events', list: 'events' },
  environments: { label: 'Environments', list: 'environments' },
  deployments: { label: 'Deployments', list: 'deployments' },
  deployment: { label: 'Deployments', list: 'deployments' },
  registries: { label: 'Registries', list: 'registries' },
  registry: { label: 'Registries', list: 'registries' },
  'registry-repository': { label: 'Registries', list: 'registries' },
  webhooks: { label: 'Notifications', list: 'webhooks' },
  tokens: { label: 'API tokens', list: 'tokens' },
  users: { label: 'Users', list: 'users' },
  audit: { label: 'Audit log', list: 'audit' },
};

// A crumb links somewhere unless it is the page itself (to is null).
const crumbs = computed(() => {
  const here = String(route.name ?? '');
  const out: { label: string; to: RouteLocationRaw | null }[] = [
    { label: 'Dashboard', to: here === 'dashboard' ? null : '/' },
  ];
  const env = typeof route.params.env === 'string' ? route.params.env : '';
  if (env) {
    const name = environments.value?.find((e) => String(e.id) === env)?.name ?? env;
    out.push({ label: name, to: here === 'env' ? null : { name: 'env', params: { env } } });
  }
  const section = sectionOf[here];
  if (section) {
    const to = here === section.list ? null : { name: section.list, params: env ? { env } : {} };
    out.push({ label: section.label, to });
  }
  return out;
});
</script>

<template>
  <nav aria-label="Breadcrumb" class="crumbs">
    <template v-for="(c, i) in crumbs" :key="i">
      <span v-if="i > 0" aria-hidden="true" class="sep">/</span>
      <RouterLink v-if="c.to" :to="c.to">{{ c.label }}</RouterLink>
      <span v-else class="current" aria-current="page">{{ c.label }}</span>
    </template>
  </nav>
</template>

<style scoped>
.crumbs {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 0.5rem;
  min-width: 0;
  font-size: 13.5px;
  color: var(--muted);
}

a {
  color: var(--muted);
  text-decoration: none;
}

a:hover {
  color: var(--text);
}

.current {
  font-weight: 500;
  color: var(--text);
}
</style>
