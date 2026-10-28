import {
  createRouter,
  createWebHistory,
  type RouteLocationNormalized,
  type RouteLocationRaw,
  type Router,
  type RouterHistory,
} from 'vue-router';

import { getSetupStatus, setUnauthorizedHandler } from './api/client';
import { apiError, currentUser, loadUser } from './session';
import AuditView from './views/AuditView.vue';
import BackupView from './views/BackupView.vue';
import ContainerFormView from './views/ContainerFormView.vue';
import ContainersView from './views/ContainersView.vue';
import ContainerView from './views/ContainerView.vue';
import DashboardView from './views/DashboardView.vue';
import DeploymentsView from './views/DeploymentsView.vue';
import DeploymentView from './views/DeploymentView.vue';
import EnvironmentsView from './views/EnvironmentsView.vue';
import EventsView from './views/EventsView.vue';
import HomeView from './views/HomeView.vue';
import ImagesView from './views/ImagesView.vue';
import ImageView from './views/ImageView.vue';
import LoginView from './views/LoginView.vue';
import NetworksView from './views/NetworksView.vue';
import NetworkView from './views/NetworkView.vue';
import NotFoundView from './views/NotFoundView.vue';
import RegistriesView from './views/RegistriesView.vue';
import RegistryRepositoryView from './views/RegistryRepositoryView.vue';
import RegistryView from './views/RegistryView.vue';
import StackNewView from './views/StackNewView.vue';
import StacksView from './views/StacksView.vue';
import StackView from './views/StackView.vue';
import SetupView from './views/SetupView.vue';
import TenantDeployView from './views/TenantDeployView.vue';
import TokensView from './views/TokensView.vue';
import UsersView from './views/UsersView.vue';
import VolumesView from './views/VolumesView.vue';
import VolumeView from './views/VolumeView.vue';
import WebhooksView from './views/WebhooksView.vue';

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
      { path: '/', name: 'dashboard', component: HomeView },
      { path: '/env/:env', name: 'env', component: DashboardView, meta: { users: true } },
      { path: '/env/:env/containers', name: 'containers', component: ContainersView, meta: { users: true } },
      { path: '/env/:env/containers/new', name: 'container-new', component: ContainerFormView },
      {
        path: '/env/:env/containers/:id',
        name: 'container',
        component: ContainerView,
        meta: { users: true },
      },
      { path: '/env/:env/containers/:id/edit', name: 'container-edit', component: ContainerFormView },
      // A user's containers go through the tenant form, built by the server from a strict spec.
      { path: '/env/:env/deploy', name: 'tenant-deploy', component: TenantDeployView, meta: { users: true } },
      {
        path: '/env/:env/containers/:id/redeploy',
        name: 'tenant-edit',
        component: TenantDeployView,
        meta: { users: true },
      },
      { path: '/env/:env/images', name: 'images', component: ImagesView },
      { path: '/env/:env/images/:id', name: 'image', component: ImageView },
      { path: '/env/:env/volumes', name: 'volumes', component: VolumesView, meta: { users: true } },
      { path: '/env/:env/volumes/:id', name: 'volume', component: VolumeView, meta: { users: true } },
      { path: '/env/:env/networks', name: 'networks', component: NetworksView, meta: { users: true } },
      { path: '/env/:env/networks/:id', name: 'network', component: NetworkView, meta: { users: true } },
      { path: '/env/:env/stacks', name: 'stacks', component: StacksView },
      // Not under stacks/: "new" is a valid stack name.
      { path: '/env/:env/new-stack', name: 'stack-new', component: StackNewView },
      { path: '/env/:env/stacks/:id', name: 'stack', component: StackView },
      { path: '/env/:env/events', name: 'events', component: EventsView, meta: { users: true } },
      { path: '/environments', name: 'environments', component: EnvironmentsView },
      { path: '/deployments', name: 'deployments', component: DeploymentsView, meta: { users: true } },
      { path: '/deployments/:name', name: 'deployment', component: DeploymentView, meta: { users: true } },
      { path: '/registries', name: 'registries', component: RegistriesView },
      { path: '/registries/:id', name: 'registry', component: RegistryView },
      // Repository names contain "/", so the name goes in the query (?name=team/app).
      { path: '/registries/:id/repository', name: 'registry-repository', component: RegistryRepositoryView },
      { path: '/webhooks', name: 'webhooks', component: WebhooksView },
      { path: '/tokens', name: 'tokens', component: TokensView, meta: { users: true } },
      { path: '/users', name: 'users', component: UsersView },
      { path: '/audit', name: 'audit', component: AuditView },
      { path: '/backup', name: 'backup', component: BackupView },
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
      return false;
    }
  });

  return router;
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
