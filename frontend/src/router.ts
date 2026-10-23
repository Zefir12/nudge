import { createRouter, createWebHistory } from 'vue-router';

import DashboardView from './views/DashboardView.vue';
import NotFoundView from './views/NotFoundView.vue';

export const router = createRouter({
  history: createWebHistory(),
  routes: [
    { path: '/', name: 'dashboard', component: DashboardView },
    { path: '/:pathMatch(.*)*', name: 'not-found', component: NotFoundView },
  ],
});
