import { createRouter, createWebHistory, type Router, type RouterHistory } from 'vue-router';

import { getSetupStatus, setUnauthorizedHandler } from './api/client';
import { currentUser, loadUser } from './session';
import DashboardView from './views/DashboardView.vue';
import LoginView from './views/LoginView.vue';
import NotFoundView from './views/NotFoundView.vue';
import SetupView from './views/SetupView.vue';
import TokensView from './views/TokensView.vue';

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
      { path: '/', name: 'dashboard', component: DashboardView },
      { path: '/tokens', name: 'tokens', component: TokensView },
      { path: '/setup', name: 'setup', component: SetupView, meta: { public: true } },
      { path: '/login', name: 'login', component: LoginView, meta: { public: true } },
      { path: '/:pathMatch(.*)*', name: 'not-found', component: NotFoundView },
    ],
  });

  // Every route but setup and login needs a user; a fresh install goes to setup first.
  router.beforeEach(async (to) => {
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
  });

  return router;
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
