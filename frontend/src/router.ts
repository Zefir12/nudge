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
import ContainersView from './views/ContainersView.vue';
import ContainerView from './views/ContainerView.vue';
import DashboardView from './views/DashboardView.vue';
import EnvironmentsView from './views/EnvironmentsView.vue';
import HomeView from './views/HomeView.vue';
import ImagesView from './views/ImagesView.vue';
import ImageView from './views/ImageView.vue';
import LoginView from './views/LoginView.vue';
import NetworksView from './views/NetworksView.vue';
import NetworkView from './views/NetworkView.vue';
import NotFoundView from './views/NotFoundView.vue';
import RegistriesView from './views/RegistriesView.vue';
import SetupView from './views/SetupView.vue';
import TokensView from './views/TokensView.vue';
import VolumesView from './views/VolumesView.vue';
import VolumeView from './views/VolumeView.vue';

declare module 'vue-router' {
  interface RouteMeta {
    // public routes are reachable without a session and shown without the sidebar.
    public?: boolean;
  }
}

// createAppRouter builds the router with its guard; tests pass a memory history.
export function createAppRouter(history: RouterHistory) {
  const router = createRouter({
    history,
    routes: [
      // The start page opens an environment; each environment's pages live under /env/:env.
      { path: '/', name: 'dashboard', component: HomeView },
      { path: '/env/:env', name: 'env', component: DashboardView },
      { path: '/env/:env/containers', name: 'containers', component: ContainersView },
      { path: '/env/:env/containers/:id', name: 'container', component: ContainerView },
      { path: '/env/:env/images', name: 'images', component: ImagesView },
      { path: '/env/:env/images/:id', name: 'image', component: ImageView },
      { path: '/env/:env/volumes', name: 'volumes', component: VolumesView },
      { path: '/env/:env/volumes/:id', name: 'volume', component: VolumeView },
      { path: '/env/:env/networks', name: 'networks', component: NetworksView },
      { path: '/env/:env/networks/:id', name: 'network', component: NetworkView },
      { path: '/environments', name: 'environments', component: EnvironmentsView },
      { path: '/registries', name: 'registries', component: RegistriesView },
      { path: '/tokens', name: 'tokens', component: TokensView },
      { path: '/setup', name: 'setup', component: SetupView, meta: { public: true } },
      { path: '/login', name: 'login', component: LoginView, meta: { public: true } },
      { path: '/:pathMatch(.*)*', name: 'not-found', component: NotFoundView },
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
  if (currentUser.value || (await loadUser())) return true;
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
