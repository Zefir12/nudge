import {
  createRouter,
  createWebHistory,
  type RouteLocationNormalized,
  type RouteLocationRaw,
  type Router,
  type RouterHistory,
} from 'vue-router';

import { getSetupStatus, setUnauthorizedHandler } from './api/client';
import { apiError, apiErrorPath, currentUser, loadUser } from './session';
import { page } from './reconnect';
// Login, setup and not-found stay in the entry chunk: they are what a visitor without a
// session sees first. Every other view loads when its route is first opened.
import LoginView from './views/LoginView.vue';
import NotFoundView from './views/NotFoundView.vue';
import SetupView from './views/SetupView.vue';

declare module 'vue-router' {
  interface RouteMeta {
    // public routes are reachable without a session and shown without the sidebar.
    public?: boolean;
    // users routes are open to users without the admin role, as on the API; every other
    // page shows them a "not allowed" state.
    users?: boolean;
  }
}

// createAppRouter builds the router with its guard; tests pass a memory history.
export function createAppRouter(history: RouterHistory) {
  const router = createRouter({
    history,
    routes: [
      // The start page opens an environment; each environment's pages live under /env/:env.
      { path: '/', name: 'dashboard', component: () => import('./views/HomeView.vue') },
      {
        path: '/env/:env',
        name: 'env',
        component: () => import('./views/DashboardView.vue'),
        meta: { users: true },
      },
      {
        path: '/env/:env/containers',
        name: 'containers',
        component: () => import('./views/ContainersView.vue'),
        meta: { users: true },
      },
      {
        path: '/env/:env/containers/new',
        name: 'container-new',
        component: () => import('./views/ContainerFormView.vue'),
      },
      {
        path: '/env/:env/containers/:id',
        name: 'container',
        component: () => import('./views/ContainerView.vue'),
        meta: { users: true },
      },
      {
        path: '/env/:env/containers/:id/edit',
        name: 'container-edit',
        component: () => import('./views/ContainerFormView.vue'),
      },
      // A user's containers go through the tenant form, built by the server from a strict spec.
      {
        path: '/env/:env/deploy',
        name: 'tenant-deploy',
        component: () => import('./views/TenantDeployView.vue'),
        meta: { users: true },
      },
      {
        path: '/env/:env/containers/:id/redeploy',
        name: 'tenant-edit',
        component: () => import('./views/TenantDeployView.vue'),
        meta: { users: true },
      },
      { path: '/env/:env/images', name: 'images', component: () => import('./views/ImagesView.vue') },
      { path: '/env/:env/images/:id', name: 'image', component: () => import('./views/ImageView.vue') },
      {
        path: '/env/:env/volumes',
        name: 'volumes',
        component: () => import('./views/VolumesView.vue'),
        meta: { users: true },
      },
      {
        path: '/env/:env/volumes/:id',
        name: 'volume',
        component: () => import('./views/VolumeView.vue'),
        meta: { users: true },
      },
      {
        path: '/env/:env/networks',
        name: 'networks',
        component: () => import('./views/NetworksView.vue'),
        meta: { users: true },
      },
      {
        path: '/env/:env/networks/:id',
        name: 'network',
        component: () => import('./views/NetworkView.vue'),
        meta: { users: true },
      },
      { path: '/env/:env/stacks', name: 'stacks', component: () => import('./views/StacksView.vue') },
      // Not under stacks/: "new" is a valid stack name.
      { path: '/env/:env/new-stack', name: 'stack-new', component: () => import('./views/StackNewView.vue') },
      { path: '/env/:env/stacks/:id', name: 'stack', component: () => import('./views/StackView.vue') },
      {
        path: '/env/:env/events',
        name: 'events',
        component: () => import('./views/EventsView.vue'),
        meta: { users: true },
      },
      // Environments are managed on the dashboard now; old links land there.
      { path: '/environments', redirect: '/' },
      {
        path: '/deployments',
        name: 'deployments',
        component: () => import('./views/DeploymentsView.vue'),
        meta: { users: true },
      },
      {
        path: '/deployments/:name',
        name: 'deployment',
        component: () => import('./views/DeploymentView.vue'),
        meta: { users: true },
      },
      { path: '/sso', name: 'sso', component: () => import('./views/OidcProvidersView.vue') },
      { path: '/registries', name: 'registries', component: () => import('./views/RegistriesView.vue') },
      { path: '/registries/:id', name: 'registry', component: () => import('./views/RegistryView.vue') },
      // Repository names contain "/", so the name goes in the query (?name=team/app).
      {
        path: '/registries/:id/repository',
        name: 'registry-repository',
        component: () => import('./views/RegistryRepositoryView.vue'),
      },
      { path: '/webhooks', name: 'webhooks', component: () => import('./views/WebhooksView.vue') },
      {
        path: '/tokens',
        name: 'tokens',
        component: () => import('./views/TokensView.vue'),
        meta: { users: true },
      },
      {
        path: '/account',
        name: 'account',
        component: () => import('./views/AccountView.vue'),
        meta: { users: true },
      },
      { path: '/users', name: 'users', component: () => import('./views/UsersView.vue') },
      { path: '/audit', name: 'audit', component: () => import('./views/AuditView.vue') },
      { path: '/backup', name: 'backup', component: () => import('./views/BackupView.vue') },
      { path: '/setup', name: 'setup', component: SetupView, meta: { public: true } },
      { path: '/login', name: 'login', component: LoginView, meta: { public: true } },
      { path: '/:pathMatch(.*)*', name: 'not-found', component: NotFoundView, meta: { users: true } },
    ],
  });

  // Every route but setup and login needs a user; a fresh install goes to setup first.
  // When the API can't answer, the navigation stops and App shows apiError.
  router.beforeEach(async (to) => {
    try {
      apiError.value = '';
      return await guard(to);
    } catch (e) {
      apiError.value = e instanceof Error ? e.message : String(e);
      apiErrorPath.value = to.fullPath;
      return false;
    }
  });

  router.afterEach((to) => clearChunkReload(to.fullPath));
  router.onError((err, to) => {
    if (isChunkLoadError(err) && markChunkReload(to.fullPath)) page.load(to.fullPath);
  });

  return router;
}

// A view's chunk fails to load when a self-update replaced the hashed files this page was
// built against: loading the page again fetches the new ones. The browsers word it
// differently; Vite's CSS preload has its own message.
const chunkLoadError = /dynamically imported module|Importing a module script failed|Unable to preload CSS/i;

export function isChunkLoadError(err: unknown): boolean {
  return err instanceof Error && chunkLoadError.test(err.message);
}

// The reload happens once per path: if the chunk is still missing afterwards, the
// navigation just fails instead of reloading forever. A successful navigation there
// clears the mark, so a later update reloads again.
const chunkReloadKey = 'nudge.chunkReload';

function markChunkReload(path: string): boolean {
  try {
    if (sessionStorage.getItem(chunkReloadKey) === path) return false;
    sessionStorage.setItem(chunkReloadKey, path);
    return true;
  } catch {
    // Without storage there is no guard against a reload loop.
    return false;
  }
}

function clearChunkReload(path: string) {
  try {
    if (sessionStorage.getItem(chunkReloadKey) === path) sessionStorage.removeItem(chunkReloadKey);
  } catch {
    // Nothing was stored.
  }
}

async function guard(to: RouteLocationNormalized): Promise<RouteLocationRaw | true> {
  if (to.meta.public) {
    if (currentUser.value) return { name: 'dashboard' };
    const { needed } = await getSetupStatus();
    if (needed && to.name !== 'setup') return { name: 'setup' };
    if (!needed && to.name === 'setup') return { name: 'login' };
    return true;
  }
  const user = currentUser.value ?? (await loadUser());
  // A user's home is the deployments: the dashboard is about environments.
  if (user) return !user.admin && to.name === 'dashboard' ? { name: 'deployments' } : true;
  const { needed } = await getSetupStatus();
  if (needed) return { name: 'setup' };
  return { name: 'login', query: to.fullPath === '/' ? {} : { redirect: to.fullPath } };
}

// redirectOnUnauthorized sends the user to login, and back afterwards, when an API call
// finds the session gone (expired, or logged out in another tab).
export function redirectOnUnauthorized(router: Router) {
  setUnauthorizedHandler(() => {
    currentUser.value = null;
    const current = router.currentRoute.value;
    if (current.meta.public) return;
    void router.push({ name: 'login', query: { redirect: current.fullPath } });
  });
}

export const router = createAppRouter(createWebHistory());
