import { flushPromises } from '@vue/test-utils';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vite-plus/test';
import { createMemoryHistory } from 'vue-router';

import { listTokens, setUnauthorizedHandler } from './api/client';
import { createAppRouter, redirectOnUnauthorized } from './router';
import { currentUser } from './session';
import { admin, stubApi, type Routes } from './test-utils';

beforeEach(() => {
  currentUser.value = null;
});

afterEach(() => {
  vi.unstubAllGlobals();
  setUnauthorizedHandler(null);
});

const unauthorized = { status: 401, body: { error: 'authentication required' } };

describe('router guard', () => {
  it.each<{ name: string; routes: Routes; to: string; want: string }>([
    {
      name: 'fresh install goes to setup',
      routes: { 'GET /auth/me': unauthorized, 'GET /setup': { body: { needed: true } } },
      to: '/tokens',
      want: '/setup',
    },
    {
      name: 'logged out goes to login with a redirect',
      routes: { 'GET /auth/me': unauthorized, 'GET /setup': { body: { needed: false } } },
      to: '/tokens?x=1',
      want: '/login?redirect=/tokens?x=1',
    },
    {
      name: 'logged out at / goes to login without a redirect',
      routes: { 'GET /auth/me': unauthorized, 'GET /setup': { body: { needed: false } } },
      to: '/',
      want: '/login',
    },
    {
      name: 'logged in passes',
      routes: { 'GET /auth/me': { body: admin } },
      to: '/tokens',
      want: '/tokens',
    },
    {
      name: 'setup after setup goes to login',
      routes: { 'GET /setup': { body: { needed: false } } },
      to: '/setup',
      want: '/login',
    },
    {
      name: 'login before setup goes to setup',
      routes: { 'GET /setup': { body: { needed: true } } },
      to: '/login',
      want: '/setup',
    },
  ])('$name', async ({ routes, to, want }) => {
    stubApi(routes);
    const router = createAppRouter(createMemoryHistory());
    await router.push(to);
    expect(decodeURIComponent(router.currentRoute.value.fullPath)).toBe(want);
  });

  it('sends a logged-in user away from login', async () => {
    currentUser.value = admin;
    stubApi({});
    const router = createAppRouter(createMemoryHistory());
    await router.push('/login');
    expect(router.currentRoute.value.name).toBe('dashboard');
  });
});

describe('401 redirect', () => {
  it('sends the user to login and back to where they were', async () => {
    stubApi({ 'GET /auth/me': { body: admin }, 'GET /tokens': unauthorized });
    const router = createAppRouter(createMemoryHistory());
    redirectOnUnauthorized(router);
    await router.push('/tokens');
    stubApi({ 'GET /tokens': unauthorized, 'GET /setup': { body: { needed: false } } });
    await expect(listTokens()).rejects.toMatchObject({ status: 401 });
    await flushPromises();
    expect(currentUser.value).toBeNull();
    expect(router.currentRoute.value.name).toBe('login');
    expect(router.currentRoute.value.query.redirect).toBe('/tokens');
  });
});
